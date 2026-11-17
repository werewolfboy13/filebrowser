package fbhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/filebrowser/filebrowser/v2/diskcache"
	"github.com/filebrowser/filebrowser/v2/settings"
	"github.com/filebrowser/filebrowser/v2/users"
)

// An upload looks for a file at its destination when it starts, and puts its
// content there when it is complete. Since the content is collected in a
// temporary file, the destination stays free in between, and the upload used
// to rename over whatever had been stored there by then: an upload that did
// not ask to override, by a user who may not modify files, replaced a file.
// Before uploads were atomic the destination was created up front and the
// second upload was the one that got 409.
func TestUploadDoesNotReplaceAFileCreatedMeanwhile(t *testing.T) {
	cases := map[string]struct {
		perm     users.Permissions
		query    string
		want     int
		replaced bool
	}{
		"no override, no Modify":   {users.Permissions{Create: true}, "", http.StatusConflict, false},
		"no override, with Modify": {users.Permissions{Create: true, Modify: true}, "", http.StatusConflict, false},
		"override without Modify":  {users.Permissions{Create: true}, "?override=true", http.StatusConflict, false},
		"override with Modify":     {users.Permissions{Create: true, Modify: true}, "?override=true", http.StatusOK, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scope := t.TempDir()
			key := []byte("test-signing-key")
			st := scopedUserStorage(t, scope, tc.perm, key)
			target := filepath.Join(scope, "report.txt")

			body := &meanwhileBody{data: "UPLOADED", meanwhile: func() {
				if pathExists(target) {
					t.Error("the upload in progress put something at its destination")
				}
				if err := os.WriteFile(target, []byte("STORED MEANWHILE"), 0o600); err != nil {
					t.Fatal(err)
				}
			}}
			req, _ := http.NewRequest(http.MethodPost, "/report.txt"+tc.query, io.NopCloser(body))
			req.Header.Set("X-Auth", signToken(t, tc.perm, key))
			rec := httptest.NewRecorder()
			handle(resourcePostHandler(diskcache.NewNoOp()), "", st, &settings.Server{}).ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			want := "STORED MEANWHILE"
			if tc.replaced {
				want = "UPLOADED"
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != want {
				t.Errorf("the destination holds %q (%v), want %q", data, err, want)
			}
			if temps := uploadTempsIn(t, scope); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
		})
	}
}

// The same for a resumable upload, where the time between the check and the
// completion is as long as the upload, and up to the uploader.
func TestTusUploadDoesNotReplaceAFileCreatedMeanwhile(t *testing.T) {
	type step func(e *fullEnv)
	none := func(*fullEnv) {}

	cases := map[string]struct {
		perm      users.Permissions
		query     string
		existing  bool // the destination exists when the upload is created
		meanwhile step
		want      int
		replaced  bool
	}{
		"no override, no Modify": {
			perm: users.Permissions{Create: true, Download: true}, want: http.StatusConflict,
		},
		"no override, with Modify": {
			perm: users.Permissions{Create: true, Modify: true, Download: true}, want: http.StatusConflict,
		},
		"override without Modify": {
			perm: users.Permissions{Create: true, Download: true}, query: "?override=true", want: http.StatusForbidden,
		},
		"override with Modify": {
			perm: users.Permissions{Create: true, Modify: true, Download: true}, query: "?override=true", want: http.StatusNoContent, replaced: true,
		},
		"override of an existing file, Modify withdrawn during the upload": {
			perm: users.Permissions{Create: true, Modify: true, Download: true}, query: "?override=true", existing: true,
			meanwhile: func(e *fullEnv) {
				e.setBob(func(u *users.User) { u.Perm.Modify = false })
			},
			want: http.StatusForbidden,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newFullEnv(t)
			e.setBob(func(u *users.User) { u.Perm = tc.perm })
			bob := e.login("bob", fullEnvUserPassword)
			admin := e.login("admin", fullEnvAdminPassword)
			target := filepath.Join(e.bobDir, "big.bin")
			if tc.existing {
				e.writeBobFile("big.bin", "ADMIN DATA")
			}

			code, hdr, body := e.do(http.MethodPost, "/api/tus/big.bin"+tc.query, bob, http.NoBody, map[string]string{"Upload-Length": "10"})
			if code != http.StatusCreated {
				t.Fatalf("tus create = %d %q", code, body)
			}
			loc := hdr.Get("Location")
			patch := func(offset int, data string) int {
				code, _, _ := e.do(http.MethodPatch, loc, bob, strings.NewReader(data), map[string]string{
					"Content-Type":  "application/offset+octet-stream",
					"Upload-Offset": strconv.Itoa(offset),
				})
				return code
			}
			if c := patch(0, "BOBBO"); c != http.StatusNoContent {
				t.Fatalf("first chunk = %d", c)
			}

			// Someone else stores a file at the destination through the API,
			// without override: nothing is there yet, so it is accepted.
			if !tc.existing {
				if code, _, body := e.do(http.MethodPost, "/api/resources/users/bob/big.bin", admin, strings.NewReader("ADMIN DATA"), nil); code != http.StatusOK {
					t.Fatalf("upload of the same new path by another user = %d %q", code, body)
				}
			}
			if tc.meanwhile == nil {
				tc.meanwhile = none
			}
			tc.meanwhile(e)

			if c := patch(5, "BBBBB"); c != tc.want {
				t.Errorf("last chunk = %d, want %d", c, tc.want)
			}
			want := "ADMIN DATA"
			if tc.replaced {
				want = "BOBBOBBBBB"
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != want {
				t.Errorf("the destination holds %q (%v), want %q", data, err, want)
			}

			// A refused upload is over: its data is discarded, and it is not
			// offered for resumption.
			if temps := uploadTempsIn(t, e.bobDir); len(temps) != 0 {
				t.Errorf("temporary files left behind: %v", temps)
			}
			if !tc.replaced {
				if code, _, _ := e.do(http.MethodHead, loc, bob, nil, nil); code != http.StatusNotFound {
					t.Errorf("HEAD of the refused upload = %d, want 404", code)
				}
			}
		})
	}
}

// What did not change: an upload to a free path is stored, with and without
// override, and an upload that asks to override an existing file replaces it.
func TestTusUploadToAFreePathIsStored(t *testing.T) {
	e := newFullEnv(t)
	e.setBob(func(u *users.User) {
		u.Perm = users.Permissions{Create: true, Download: true} // no Modify
	})
	bob := e.login("bob", fullEnvUserPassword)

	for _, query := range []string{"", "?override=true"} {
		name := "new" + strconv.Itoa(len(query)) + ".bin"
		code, hdr, body := e.do(http.MethodPost, "/api/tus/"+name+query, bob, http.NoBody, map[string]string{"Upload-Length": "4"})
		if code != http.StatusCreated {
			t.Fatalf("tus create%s = %d %q", query, code, body)
		}
		code, _, _ = e.do(http.MethodPatch, hdr.Get("Location"), bob, strings.NewReader("DATA"), map[string]string{
			"Content-Type": "application/offset+octet-stream", "Upload-Offset": "0",
		})
		if code != http.StatusNoContent {
			t.Errorf("upload%s to a free path = %d, want 204", query, code)
		}
		if data, _ := os.ReadFile(filepath.Join(e.bobDir, name)); string(data) != "DATA" {
			t.Errorf("%s holds %q", name, data)
		}
		info, err := os.Stat(filepath.Join(e.bobDir, name))
		if err != nil || info.Mode().Perm() != settings.DefaultFileMode {
			t.Errorf("%s: %v, %v; want mode %v", name, info, err, settings.DefaultFileMode)
		}
	}
	if temps := uploadTempsIn(t, e.bobDir); len(temps) != 0 {
		t.Errorf("temporary files left behind: %v", temps)
	}
}
