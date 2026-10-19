package server

import (
	"archive/zip"
	"bytes"
	"context"
	"github.com/vestigiumincaligne/polka/internal/library"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vestigiumincaligne/polka/internal/config"
	"github.com/vestigiumincaligne/polka/internal/store"
)

func testEPUB(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range []struct{ name, data string }{
		{"mimetype", "application/epub+zip"},
		{"META-INF/container.xml", `<container><rootfiles><rootfile full-path="book.opf"/></rootfiles></container>`},
		{"book.opf", `<package version="3.0"/>`},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEPUBDownload(t *testing.T) {
	for _, mode := range []string{"success", "failure", "invalid", "non-fb2"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			expected := testEPUB(t)
			fixture := filepath.Join(dir, "fixture.epub")
			if err := os.WriteFile(fixture, expected, 0o600); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(dir, "calls")
			binary := filepath.Join(dir, "fbc")
			t.Setenv("EPUB_FIXTURE", fixture)
			t.Setenv("EPUB_CALLS", calls)
			script := `#!/bin/sh
set -eu
[ "$1" = convert ] && [ "$2" = --to ] && [ "$3" = epub3 ] && [ "$4" = --output-file ]
grep -q FictionBook "$6"
printf 'call\n' >> "$EPUB_CALLS"
cp "$EPUB_FIXTURE" "$5"
`
			if mode == "failure" {
				script += "exit 1\n"
			}
			if mode == "invalid" {
				script += "printf broken > \"$5\"\n"
			}
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ts, id := newTestServer(t, func(cfg *config.Config, st *store.Store) {
				cfg.DataDir = dir
				cfg.FBCPath = binary
				if mode == "non-fb2" {
					if _, err := st.DB().Exec("UPDATE books SET ext = 'pdf'"); err != nil {
						t.Fatal(err)
					}
				}
			})
			url := ts.URL + "/Images/epub/" + itoa64(id)
			request := func() {
				t.Helper()
				resp, err := http.Get(url)
				if err != nil {
					t.Error(err)
					return
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Error(err)
					return
				}
				wantStatus := http.StatusOK
				if mode == "failure" || mode == "invalid" {
					wantStatus = http.StatusInternalServerError
				}
				if mode == "non-fb2" {
					wantStatus = http.StatusUnsupportedMediaType
				}
				if resp.StatusCode != wantStatus {
					t.Errorf("status = %d, want %d: %s", resp.StatusCode, wantStatus, body)
				}
				if mode == "success" {
					if resp.Header.Get("Content-Type") != "application/epub+zip" || !strings.Contains(resp.Header.Get("Content-Disposition"), "100.epub") {
						t.Errorf("headers = %v", resp.Header)
					}
					if !bytes.Equal(body, expected) {
						t.Error("unexpected EPUB body")
					}
				}
			}
			var wg sync.WaitGroup
			for range 6 {
				wg.Add(1)
				go func() { defer wg.Done(); request() }()
			}
			wg.Wait()
			request()
			cache := filepath.Join(dir, "epub-cache", itoa64(id)+".epub")
			if mode == "success" {
				data, err := os.ReadFile(calls)
				if err != nil || string(data) != "call\n" {
					t.Fatalf("conversion calls = %q, %v", data, err)
				}
				if err := os.WriteFile(cache, []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
				request()
				data, _ = os.ReadFile(calls)
				if string(data) != "call\ncall\n" {
					t.Errorf("invalid cache not rebuilt: %q", data)
				}
			} else if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Errorf("failed conversion left final cache: %v", err)
			}
			if mode == "non-fb2" {
				if _, err := os.Stat(calls); !os.IsNotExist(err) {
					t.Error("non-FB2 passed to converter")
				}
			}
			entries, _ := os.ReadDir(filepath.Join(dir, "epub-cache"))
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".convert-") {
					t.Errorf("temporary files left: %s", entry.Name())
				}
			}
		})
	}
}

func TestOPDSEpubAcquisition(t *testing.T) {
	for _, ext := range []string{"fb2", "FB2", "epub", "pdf", "txt"} {
		entry := opdsBookEntry(store.Book{ID: 7, Ext: ext})
		found := false
		count := 0
		for _, link := range entry.Links {
			if link.Rel == relAcquisition {
				count++
			}
			if link.Href == "/Images/epub/7" && link.Type == "application/epub+zip" && link.Rel == relAcquisition {
				found = true
			}
		}
		fb2 := strings.EqualFold(ext, "fb2")
		if found != fb2 || (fb2 && count != 3) || (!fb2 && count != 1) {
			t.Errorf("%s links: %+v", ext, entry.Links)
		}
	}
}

func TestEPUBConversionCancellation(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fbc")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var s Server
	var st *store.Store
	_, id := newTestServer(t, func(cfg *config.Config, database *store.Store) {
		cfg.DataDir, cfg.FBCPath = dir, binary
		s.cfg, s.lib, st = cfg, library.New(cfg.LibraryDir), database
	})
	book, err := st.BookFile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := s.cachedEPUB(ctx, book); err == nil {
		t.Fatal("conversion succeeded despite cancellation")
	}
	if ctx.Err() == nil || time.Since(start) > 3*time.Second {
		t.Fatal("conversion did not terminate on cancellation")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "epub-cache"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache after cancellation: %v, %v", entries, err)
	}
}
