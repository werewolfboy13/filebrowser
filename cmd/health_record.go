package cmd

import (
	"bytes"
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

	"github.com/filebrowser/filebrowser/v2/settings"
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
func recordListenAddress(server *settings.Server, listener net.Listener) (remove func()) {
	path := os.Getenv(envHealthFile)
	if path == "" {
		return func() {}
	}

	if err := writeListenRecord(path, newListenRecord(server, listener)); err != nil {
		// What an earlier server left there says nothing about this one.
		_ = os.Remove(path)
		log.Printf("WARNING: can't write %s (%v). The health command will look for the server", path, err)
		log.Printf("WARNING: in its own flags, the environment and the configuration file instead.")
		return func() {}
	}

	return func() { _ = os.Remove(path) }
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

// readListenRecord returns the record at path, if there is one and the server
// that wrote it is still running.
func readListenRecord(path string) (*listenRecord, bool) {
	if path == "" {
		return nil, false
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, listenRecordMaxSize))
	if err != nil {
		return nil, false
	}

	record := &listenRecord{}
	if err := json.Unmarshal(data, record); err != nil {
		return nil, false
	}
	if record.Socket == "" && record.Port == "" {
		return nil, false
	}

	// A server that was killed could not remove its record. Where the start
	// time of a process can be read, the record counts only while the process
	// that wrote it is the one with that number now.
	if record.StartTime != "" && processStartTime(record.PID) != record.StartTime {
		return nil, false
	}

	return record, true
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
