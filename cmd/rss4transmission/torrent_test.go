package main

import (
	"fmt"
	"testing"
)

func bencodeString(s string) string {
	return fmt.Sprintf("%d:%s", len(s), s)
}

func bencodeInt(n int) string {
	return fmt.Sprintf("i%de", n)
}

// buildMultiFileTorrent returns raw bencoded bytes for a multi-file torrent.
// Each entry in files is a slice of path components.
func buildMultiFileTorrent(name string, files [][]string) []byte {
	filesList := "l"
	for _, pathParts := range files {
		pathList := "l"
		for _, part := range pathParts {
			pathList += bencodeString(part)
		}
		pathList += "e"
		filesList += "d" +
			bencodeString("length") + bencodeInt(1000) +
			bencodeString("path") + pathList +
			"e"
	}
	filesList += "e"

	infoDict := "d" +
		bencodeString("files") + filesList +
		bencodeString("name") + bencodeString(name) +
		bencodeString("piece length") + bencodeInt(262144) +
		bencodeString("pieces") + bencodeString("xxxx") +
		"e"

	return []byte("d" +
		bencodeString("announce") + bencodeString("http://tracker.example.com/announce") +
		bencodeString("info") + infoDict +
		"e")
}

// buildSingleFileTorrent returns raw bencoded bytes for a single-file torrent.
func buildSingleFileTorrent(name string) []byte {
	infoDict := "d" +
		bencodeString("length") + bencodeInt(1000000) +
		bencodeString("name") + bencodeString(name) +
		bencodeString("piece length") + bencodeInt(262144) +
		bencodeString("pieces") + bencodeString("xxxx") +
		"e"

	return []byte("d" +
		bencodeString("announce") + bencodeString("http://tracker.example.com/announce") +
		bencodeString("info") + infoDict +
		"e")
}

func TestTorrentFileNames_MultiFile(t *testing.T) {
	data := buildMultiFileTorrent("MotoGP.2024.RD01", [][]string{
		{"MotoGP.2024.RD01.Qatar.Race.TNT.1080p.mkv"},
		{"Moto2.2024.RD01.Qatar.Race.TNT.1080p.mkv"},
		{"Moto3.2024.RD01.Qatar.Race.TNT.1080p.mkv"},
	})
	names, err := TorrentFileNames(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 file names, got %d: %v", len(names), names)
	}
	wantNames := map[string]bool{
		"MotoGP.2024.RD01.Qatar.Race.TNT.1080p.mkv": true,
		"Moto2.2024.RD01.Qatar.Race.TNT.1080p.mkv":  true,
		"Moto3.2024.RD01.Qatar.Race.TNT.1080p.mkv":  true,
	}
	for _, name := range names {
		if !wantNames[name] {
			t.Errorf("unexpected file name: %q", name)
		}
	}
}

func TestTorrentFileNames_SingleFile(t *testing.T) {
	data := buildSingleFileTorrent("MotoGP.2024.RD01.Qatar.Race.TNT.1080p.mkv")
	names, err := TorrentFileNames(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 file name, got %d: %v", len(names), names)
	}
	if names[0] != "MotoGP.2024.RD01.Qatar.Race.TNT.1080p.mkv" {
		t.Errorf("name = %q, want MotoGP.2024.RD01.Qatar.Race.TNT.1080p.mkv", names[0])
	}
}

func TestTorrentFileNames_InvalidData(t *testing.T) {
	_, err := TorrentFileNames([]byte("not bencode"))
	if err == nil {
		t.Error("expected error for invalid bencode")
	}
}

func TestBencodeDecodeString_NegativeLength(t *testing.T) {
	// Malformed bencode with a negative length must return an error, not panic.
	_, _, err := bencodeDecodeString([]byte("-1:xx"), 0)
	if err == nil {
		t.Error("expected error for negative string length")
	}
}

func TestTorrentFileNames_NestedPath(t *testing.T) {
	data := buildMultiFileTorrent("Bundle", [][]string{
		{"subdir", "MotoGP.2024.RD01.Qatar.Race.TNT.1080p.mkv"},
	})
	names, err := TorrentFileNames(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 file name, got %d", len(names))
	}
	// Only the leaf file name (last path component) is returned.
	if names[0] != "MotoGP.2024.RD01.Qatar.Race.TNT.1080p.mkv" {
		t.Errorf("name = %q, want leaf file name", names[0])
	}
}
