//go:build windows

package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scratchpad/internal/store"
	"scratchpad/internal/testutil"
)

// TestListFragmentBrowsesJunctionWatch is the junction-flavour twin of
// TestListFragmentBrowsesWatchWithoutFollowingNestedLinks (server_test.go).
// A junction is the default watch-link flavour for any Developer-Mode-off
// Windows user (docs/windows.md), yet before this test internal/web had
// zero junction coverage — all three of its link tests used
// testutil.RequireSymlinks (P4.7 semantic-parity finding P-4).
//
// The link is planted directly with testutil.MakeJunction rather than
// through store.Watch: Watch tries a directory symlink FIRST and falls
// back to a junction only when that fails, so on a Developer-Mode-on
// runner it would silently produce a symlink instead and defeat a test
// whose whole point is the junction flavour specifically.
func TestListFragmentBrowsesJunctionWatch(t *testing.T) {
	testutil.RequireWatchLinks(t)
	root := testRoot(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "readme.md"), []byte("# watched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testutil.MakeJunction(filepath.Join(root, "watched"), source); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, notesMux(t), http.MethodGet, "/fragments/list?project=watched", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "readme") {
		t.Fatalf("junction-watched markdown missing from response: %s", rec.Body.String())
	}
}

// TestJunctionWatchListedOffersUnwatchNotDeleteAndIsUnwatchable proves the
// remaining properties P-4 asks internal/web to cover for a junction-backed
// watch — the same ones the symlink flavour already gets through
// TestWatchViaJunctionIsListedAndUnwatchable at the store layer
// (internal/store/storefs_windows_attack_test.go) plus this package's own
// unwatch-affordance rendering, which that store-level test cannot reach:
//
//   - listed: the root list fragment includes the card
//   - offers unwatch, not delete: the rendered button hx-deletes
//     /watch/{path}, never /a/{path}
//   - browsable: the artifact document serves over HTTP
//   - unwatchable: DELETE /watch/{path} succeeds and the source survives
func TestJunctionWatchListedOffersUnwatchNotDeleteAndIsUnwatchable(t *testing.T) {
	testutil.RequireWatchLinks(t)
	root := testRoot(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<h1>src</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testutil.MakeJunction(filepath.Join(root, "viajunction"), source); err != nil {
		t.Fatal(err)
	}

	mux := notesMux(t)

	rec := doReq(t, mux, http.MethodGet, "/fragments/list", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "viajunction") {
		t.Fatalf("junction-watched artifact not listed: %s", body)
	}
	if !strings.Contains(body, `hx-delete="/watch/viajunction"`) {
		t.Fatalf("junction-watched artifact did not offer unwatch: %s", body)
	}
	if strings.Contains(body, `hx-delete="/a/viajunction"`) {
		t.Fatalf("junction-watched artifact offered delete instead of unwatch: %s", body)
	}

	rec = doReq(t, mux, http.MethodGet, "/a/viajunction/index.html", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /a/viajunction/index.html = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, mux, http.MethodDelete, "/watch/viajunction", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /watch/viajunction = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(source, "index.html")); err != nil {
		t.Errorf("source must survive unwatching a junction: %v", err)
	}
	if links, err := store.Watches(); err != nil || len(links) != 0 {
		t.Errorf("Watches() = %+v, err %v; want none left after unwatch", links, err)
	}
}
