package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/werewolfboy13/filebrowser/v2/settings"
)

const (
	// envHealthFile names the file in which the running server writes down
	// where it listens, and in which the health command looks it up. Nothing
	// is written when it is empty.
	envHealthFile = "FB_HEALTH_FILE"

	listenRecordPermissions = 0644
	// listenRecordMaxSize is far more than a record needs. It keeps the
	// health command from reading a file that is something else.
	listenRecordMaxSize = 64 * 1024

	// hostLookupTimeout bounds the lookup of a recorded host name. It comes
	// on top of healthTimeout, and the two together stay below the timeout
	// of the Docker image's HEALTHCHECK. The name of a container is in its
	// /etc/hosts, which takes no time to read.
	hostLookupTimeout = 500 * time.Millisecond
)

// How the addresses of a host name and of this machine's interfaces are
// looked up. They are variables so that the tests can do without the network
// and without the interfaces of the machine they run on.
var (
	lookupHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	interfaceAddrs = net.InterfaceAddrs
)

// listenRecord is what the running server writes down for the health
// command: where it listens, as it worked that out from its flags, the
// environment, the configuration file and the database.
//
// The health command runs as another process, and sees neither the flags the
// server was started with nor the database, which the server holds locked.
// So it cannot work this out again by itself. In the Docker image, a server
// started with `--port 8080` or `--config /config/custom.json` was reported
// as unhealthy for as long as it ran.
type listenRecord struct {
	// PID and StartTime say which process wrote the record, so that a record
	// left behind by a server that was killed is not taken for the running
	// one's. StartTime is empty where it cannot be read (see processStartTime).
	PID       int    `json:"pid"`
	StartTime string `json:"startTime,omitempty"`

	Address string `json:"address"`
	Port    string `json:"port"`
	Socket  string `json:"socket,omitempty"`
	TLS     bool   `json:"tls"`
	BaseURL string `json:"baseURL"`
}

// listen returns the record as the settings the health check is made from.
func (r *listenRecord) listen() listenSettings {
	return listenSettings{
		address: r.Address,
		port:    r.Port,
		socket:  r.Socket,
		tls:     r.TLS,
		baseURL: r.BaseURL,
	}
}

// newListenRecord describes a server that listens on listener with the
// settings in server.
func newListenRecord(server *settings.Server, listener net.Listener) *listenRecord {
	record := &listenRecord{
		PID:       os.Getpid(),
		StartTime: processStartTime(os.Getpid()),
		BaseURL:   server.BaseURL,
	}

	// As the server chooses between them: a socket, or else an address and a
	// port, with TLS when it has both a certificate and a key.
	if server.Socket != "" {
		record.Socket = server.Socket
		return record
	}

	record.Address = server.Address
	record.Port = server.Port
	record.TLS = server.TLSKey != "" && server.TLSCert != ""

	// The port the server got, which is not the configured one when that is
	// 0 or the name of a service.
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		record.Port = strconv.Itoa(addr.Port)
	}

	return record
}

// recordListenAddress writes down where the server listens, in the file that
// FB_HEALTH_FILE names, and returns the function that removes the file again
// when the server stops. It does nothing when the variable is empty.
//
// A file that cannot be written is not an error: the health command then
// looks for the server as it did before there was a record.
//
// The record of another server that is still running is left as it is, and
// so is, when this server stops, a record that is no longer its own. A second
// server in the same container, started with `docker exec` on a port and a
// database of its own, took the record over and removed it when it stopped:
// the container's own server was then looked for where it does not listen,
// and reported as unhealthy until the container was restarted.
func recordListenAddress(server *settings.Server, listener net.Listener) (remove func()) {
	path := os.Getenv(envHealthFile)
	if path == "" {
		return func() {}
	}

	record := newListenRecord(server, listener)

	// With the checks of the health command, so only a record that it would
	// use counts. Without a start time there is no telling a server that
	// runs from one that was killed, and the record is replaced, as before.
	if other, ok := readListenRecord(path); ok && other.StartTime != "" && !other.writtenBy(record) {
		log.Printf("WARNING: %s is the record of another File Browser that is running (process %d).", path, other.PID)
		log.Printf("WARNING: It is left as it is: the health command checks that server, not this one.")
		return func() {}
	}

	if err := writeListenRecord(path, record); err != nil {
		// What an earlier server left there says nothing about this one.
		// Should it not be this server's to remove either, the health
		// command does not take its word: see readListenRecord.
		_ = os.Remove(path)
		log.Printf("WARNING: can't write %s (%v). The health command will look for the server", path, err)
		log.Printf("WARNING: in its own flags, the environment and the configuration file instead.")
		return func() {}
	}

	return func() {
		if current, _, ok := loadListenRecord(path); ok && current.writtenBy(record) {
			_ = os.Remove(path)
		}
	}
}

// writtenBy reports whether the record comes from the process that wrote
// other.
func (r *listenRecord) writtenBy(other *listenRecord) bool {
	return r.PID == other.PID && r.StartTime == other.StartTime
}

// writeListenRecord writes the record to path in one step, so that the health
// command never reads half of it.
func writeListenRecord(path string, record *listenRecord) (err error) {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}

	// A new file with a name of its own, also when the directory is one that
	// other users can write to, such as /tmp: it is never an existing file or
	// link of theirs. Renaming it replaces a link at path, and does not follow
	// it.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return err
	}
	// Readable by whoever runs the health command, which can be another user
	// than the server's.
	if err = tmp.Chmod(listenRecordPermissions); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

// loadListenRecord returns what the file at path holds as a record, and the
// file's details. It checks neither who wrote it nor whether that process
// still runs: see readListenRecord.
func loadListenRecord(path string) (*listenRecord, os.FileInfo, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, false
	}

	data, err := io.ReadAll(io.LimitReader(file, listenRecordMaxSize))
	if err != nil {
		return nil, nil, false
	}

	record := &listenRecord{}
	if err := json.Unmarshal(data, record); err != nil {
		return nil, nil, false
	}

	return record, info, true
}

// readListenRecord returns the record at path, if there is one, the server
// that wrote it is still running, and it is that server's.
//
// The health command sends its request where the record says, so a record
// is only as good as whoever wrote it. The file is not always the server's:
// where it cannot replace one that is already there, it goes on without (see
// recordListenAddress), and what is there stays.
func readListenRecord(path string) (*listenRecord, bool) {
	if path == "" {
		return nil, false
	}

	record, info, ok := loadListenRecord(path)
	if !ok {
		return nil, false
	}
	switch {
	case record.Socket != "":
	case record.Port == "":
		return nil, false
	default:
		// The server records the number of the port it got. Anything else
		// in its place would end up in the address of the request.
		if _, err := strconv.ParseUint(record.Port, 10, 16); err != nil {
			return nil, false
		}
	}

	// A server that was killed could not remove its record. Where the start
	// time of a process can be read, the record counts only while the process
	// that wrote it is the one with that number now. There, a record says
	// when its process started: one that does not was not written by a
	// server on this system.
	if record.StartTime == "" && processStartTime(os.Getpid()) != "" {
		return nil, false
	}
	if record.StartTime != "" && processStartTime(record.PID) != record.StartTime {
		return nil, false
	}

	if !recordOwnerTrusted(info, record.PID) {
		return nil, false
	}

	return record, true
}

// localAddresses reports whether address, as a server is configured with it,
// is one of this machine: none at all (every address), localhost, a loopback
// address, an address of one of its interfaces, or a host name that stands
// for one of those. A server can listen on no other, so a record that names
// another did not come from one.
//
// For a host name, it also returns the addresses of the name that are this
// machine's. A name can stand for other machines as well, and the health
// command is to connect to none of those: see healthTarget.
func localAddresses(address string) (ofName []string, local bool) {
	host := strings.TrimSuffix(strings.TrimPrefix(address, "["), "]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return nil, true
	}

	if ip := net.ParseIP(host); ip != nil {
		return nil, isLocalIP(ip)
	}

	// A host name, such as the container's own with `--address <name>`. The
	// server listens on an address of that name, so one of them is this
	// machine's. A name that stands for other machines only, or for none,
	// is no more local than their addresses are.
	ctx, cancel := context.WithTimeout(context.Background(), hostLookupTimeout)
	defer cancel()

	ips, err := lookupHostIPs(ctx, host)
	if err != nil {
		return nil, false
	}
	for _, ip := range ips {
		if isLocalIP(ip) {
			ofName = append(ofName, ip.String())
		}
	}
	return ofName, len(ofName) != 0
}

// isLocalIP reports whether ip is an address of this machine: every address,
// a loopback address, or an address of one of its interfaces.
func isLocalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}

	addrs, err := interfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// processStartTime returns the time at which the process started, as the
// kernel counts it, or "" where that cannot be read: it comes from /proc,
// which only Linux has in this form. With the process number, it tells one
// process from every other one since the machine started.
func processStartTime(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}

	startTime, err := parseProcStatStartTime(data)
	if err != nil {
		return ""
	}

	return startTime
}

// parseProcStatStartTime returns field 22 of a /proc/<pid>/stat line, the
// start time of the process in clock ticks since the machine started.
func parseProcStatStartTime(stat []byte) (string, error) {
	// Field 2 is the name of the program in parentheses, and can itself hold
	// spaces and parentheses. The fields after it are plain.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return "", errors.New("no program name in the stat line")
	}

	// fields[0] is field 3.
	const startTimeIndex = 22 - 3
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= startTimeIndex {
		return "", errors.New("the stat line is too short")
	}

	if _, err := strconv.ParseUint(fields[startTimeIndex], 10, 64); err != nil {
		return "", err
	}

	return fields[startTimeIndex], nil
}
