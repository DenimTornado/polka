package server

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vestigiumincaligne/polka/internal/store"
)

func (s *Server) handleBookEPUB(w http.ResponseWriter, r *http.Request) {
	book := s.bookFileOr404(w, r)
	if book == nil {
		return
	}
	if !strings.EqualFold(book.Ext, "fb2") {
		http.Error(w, "EPUB conversion requires FB2", http.StatusUnsupportedMediaType)
		return
	}
	path, err := s.cachedEPUB(r.Context(), book)
	if err != nil {
		s.apiError(w, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		s.apiError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		s.apiError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/epub+zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": book.File + ".epub"}))
	http.ServeContent(w, r, book.File+".epub", info.ModTime(), file)
}

func (s *Server) cachedEPUB(ctx context.Context, book *store.BookFile) (string, error) {
	// Fixed lock stripes bound memory while serializing requests for the same book.
	lock := &s.epubLocks[uint64(book.ID)%uint64(len(s.epubLocks))]
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir := filepath.Join(s.cfg.DataDir, "epub-cache")
	path := filepath.Join(dir, strconv.FormatInt(book.ID, 10)+".epub")
	if err := validateEPUB(path); err == nil {
		return path, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(dir, ".convert-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	source, _, err := s.lib.Open(book.Folder, book.File, book.Ext)
	if err != nil {
		return "", err
	}
	input := filepath.Join(work, "input.fb2")
	err = writeEPUBSource(input, source)
	source.Close()
	if err != nil {
		return "", err
	}
	output := filepath.Join(work, "output.epub")
	binary := s.cfg.FBCPath
	if binary == "" {
		binary = "/usr/local/bin/fbc"
	}
	if err := exec.CommandContext(ctx, binary, "convert", "--to", "epub3", "--output-file", output, input).Run(); err != nil {
		return "", fmt.Errorf("convert book %d to EPUB: %w", book.ID, err)
	}
	if err := validateEPUB(output); err != nil {
		return "", fmt.Errorf("invalid converted EPUB for book %d: %w", book.ID, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(output, path); err != nil {
		return "", err
	}
	return path, nil
}

func writeEPUBSource(path string, source io.Reader) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func validateEPUB(path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	if len(archive.File) == 0 || archive.File[0].Name != "mimetype" || archive.File[0].Method != zip.Store {
		return fmt.Errorf("missing uncompressed EPUB mimetype")
	}
	hasContainer := false
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		if entry.Name == "mimetype" {
			data, readErr := io.ReadAll(io.LimitReader(reader, 64))
			reader.Close()
			if readErr != nil {
				return readErr
			}
			if string(data) != "application/epub+zip" {
				return fmt.Errorf("invalid EPUB mimetype")
			}
		} else {
			_, readErr := io.Copy(io.Discard, reader)
			reader.Close()
			if readErr != nil {
				return readErr
			}
		}
		if entry.Name == "META-INF/container.xml" {
			hasContainer = true
		}
	}
	if !hasContainer {
		return fmt.Errorf("missing EPUB container")
	}
	return nil
}
