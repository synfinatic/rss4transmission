package main

import (
	"fmt"
	"strconv"
)

// TorrentFileNames returns the file names from a raw .torrent file. For
// single-file torrents it returns the torrent name. For multi-file torrents it
// returns the last path component of each file entry.
func TorrentFileNames(data []byte) ([]string, error) {
	val, _, err := bencodeDecode(data, 0)
	if err != nil {
		return nil, fmt.Errorf("invalid torrent data: %w", err)
	}
	dict, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("torrent root is not a dict")
	}
	infoRaw, ok := dict["info"]
	if !ok {
		return nil, fmt.Errorf("torrent has no info dict")
	}
	info, ok := infoRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("torrent info is not a dict")
	}

	filesRaw, hasFiles := info["files"]
	if !hasFiles {
		// Single-file torrent.
		name, ok := info["name"].(string)
		if !ok {
			return nil, fmt.Errorf("torrent name is not a string")
		}
		return []string{name}, nil
	}

	// Multi-file torrent.
	files, ok := filesRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("torrent files is not a list")
	}
	var names []string
	for _, fileRaw := range files {
		fileDict, ok := fileRaw.(map[string]interface{})
		if !ok {
			continue
		}
		pathRaw, ok := fileDict["path"].([]interface{})
		if !ok || len(pathRaw) == 0 {
			continue
		}
		leaf, ok := pathRaw[len(pathRaw)-1].(string)
		if !ok {
			continue
		}
		names = append(names, leaf)
	}
	return names, nil
}

// bencodeDecode decodes a single bencoded value from data starting at pos.
// Returns (value, nextPos, error).
func bencodeDecode(data []byte, pos int) (interface{}, int, error) {
	if pos >= len(data) {
		return nil, pos, fmt.Errorf("unexpected end of data at position %d", pos)
	}
	switch data[pos] {
	case 'i':
		return bencodeDecodeInt(data, pos)
	case 'l':
		return bencodeDecodeList(data, pos)
	case 'd':
		return bencodeDecodeDict(data, pos)
	default:
		return bencodeDecodeString(data, pos)
	}
}

func bencodeDecodeInt(data []byte, pos int) (int64, int, error) {
	pos++ // skip 'i'
	end := pos
	for end < len(data) && data[end] != 'e' {
		end++
	}
	if end >= len(data) {
		return 0, pos, fmt.Errorf("unterminated integer at position %d", pos)
	}
	n, err := strconv.ParseInt(string(data[pos:end]), 10, 64)
	if err != nil {
		return 0, pos, fmt.Errorf("invalid integer: %w", err)
	}
	return n, end + 1, nil
}

func bencodeDecodeString(data []byte, pos int) (string, int, error) {
	end := pos
	for end < len(data) && data[end] != ':' {
		end++
	}
	if end >= len(data) {
		return "", pos, fmt.Errorf("invalid string at position %d", pos)
	}
	length, err := strconv.Atoi(string(data[pos:end]))
	if err != nil {
		return "", pos, fmt.Errorf("invalid string length at position %d: %w", pos, err)
	}
	if length < 0 {
		return "", pos, fmt.Errorf("negative string length at position %d", pos)
	}
	start := end + 1
	if start+length > len(data) {
		return "", pos, fmt.Errorf("string extends beyond data at position %d", pos)
	}
	return string(data[start : start+length]), start + length, nil
}

func bencodeDecodeList(data []byte, pos int) ([]interface{}, int, error) {
	pos++ // skip 'l'
	var items []interface{}
	for pos < len(data) && data[pos] != 'e' {
		item, newPos, err := bencodeDecode(data, pos)
		if err != nil {
			return nil, pos, err
		}
		items = append(items, item)
		pos = newPos
	}
	if pos >= len(data) {
		return nil, pos, fmt.Errorf("unterminated list")
	}
	return items, pos + 1, nil
}

func bencodeDecodeDict(data []byte, pos int) (map[string]interface{}, int, error) {
	pos++ // skip 'd'
	dict := make(map[string]interface{})
	for pos < len(data) && data[pos] != 'e' {
		key, newPos, err := bencodeDecodeString(data, pos)
		if err != nil {
			return nil, pos, fmt.Errorf("dict key: %w", err)
		}
		pos = newPos
		val, newPos, err := bencodeDecode(data, pos)
		if err != nil {
			return nil, pos, fmt.Errorf("dict value for %q: %w", key, err)
		}
		dict[key] = val
		pos = newPos
	}
	if pos >= len(data) {
		return nil, pos, fmt.Errorf("unterminated dict")
	}
	return dict, pos + 1, nil
}
