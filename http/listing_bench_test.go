package fbhttp

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/werewolfboy13/filebrowser/v3/rules"
	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// BenchmarkListingWithRulesAndProtectedPaths times the listing of a
// directory of 3000 files for a user with 30 deny rules, half of them not
// ASCII, on an instance with five of its own files registered as protected:
// one request per iteration, through the whole server.
//
// Every entry of a listing is checked against every rule and every protected
// path. When deny rules and protected paths came to be compared on canonical
// names, that was worked out again for each comparison, and a listing of
// names that are not ASCII took five to six times as long.
func BenchmarkListingWithRulesAndProtectedPaths(b *testing.B) {
	const entries = 3000

	for _, fold := range []bool{false, true} {
		for _, names := range []struct{ name, format string }{
			{"ascii", "report-%05d-final-version.txt"},
			{"not ascii", "\u5831\u544a\u66f8-%05d-\u6700\u7d42\u7248-Gr\u00f6\u00dfe-\u00e9.txt"},
		} {
			b.Run(fmt.Sprintf("fold %v, %s", fold, names.name), func(b *testing.B) {
				e := newFullEnvOpts(b, fullEnvOptions{configure: func(root string, s *settings.Server) {
					for _, p := range []string{"filebrowser.db", "config/settings.json", "cache", "tls/key.pem"} {
						s.Protect(filepath.Join(root, p))
					}
					s.ProtectLog(filepath.Join(root, "logs", "filebrowser.log"))
				}})
				// The rules decide by the name alone, whatever the
				// filesystem under the test does.
				e.server.CaseInsensitiveFs = fold

				dir := filepath.Join(e.bobDir, "big")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					b.Fatal(err)
				}
				for i := 0; i < entries; i++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(names.format, i)), nil, 0o644); err != nil {
						b.Fatal(err)
					}
				}

				var list []rules.Rule
				for i := 0; i < 30; i++ {
					p := fmt.Sprintf("/private-%02d", i)
					if i%2 == 0 {
						p = fmt.Sprintf("/\u6a5f\u5bc6-%02d-Stra\u00dfe", i)
					}
					list = append(list, rules.Rule{Path: p})
				}
				e.setBob(func(u *users.User) { u.Rules = list })
				tok := e.login("bob", fullEnvUserPassword)

				list1 := func() {
					if code, _, body := e.do(http.MethodGet, "/api/resources/big/", tok, nil, nil); code != http.StatusOK || len(body) < entries*20 {
						b.Fatalf("listing = %d, %d bytes", code, len(body))
					}
				}
				list1() // warm up
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					list1()
				}
			})
		}
	}
}
