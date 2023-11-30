package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type TreeEntry struct {
	mode string
	name string
	sha  string
}

func nextTreeEntry(br *bufio.Reader) (TreeEntry, error) {
	modeBytes, err := br.ReadBytes(' ')
	if err != nil {
		return TreeEntry{}, err
	}
	mode := string(modeBytes[:len(modeBytes)-1]) // remove trailing space
	nameBytes, err := br.ReadBytes('\x00')
	if err != nil {
		return TreeEntry{}, err
	}
	name := string(nameBytes[:len(nameBytes)-1]) // remove trailing null

	shaBytes := [20]byte{}
	_, err = br.Read(shaBytes[:])
	if err != nil {
		return TreeEntry{}, err
	}
	sha := fmt.Sprintf("%x", shaBytes)
	return TreeEntry{
		mode: mode,
		name: name,
		sha:  sha,
	}, nil
}

func hashObject(filename string) int {
	s, err := os.Stat(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting file info: %s\n", err)
		os.Exit(1)
	}
	fileLength := s.Size()
	// Reader 1 - header
	headerReader := strings.NewReader(fmt.Sprintf("blob %d\x00", fileLength))
	// Reader 2 - file content
	fileReader, err := os.Open(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening file: %s\n", err)
		os.Exit(1)
	}
	defer fileReader.Close()

	reader := io.MultiReader(headerReader, fileReader)
	// Writer 1 - hash
	hashWriter := sha1.New()
	// Writer 2 - zlib
	fileWriter, err := os.CreateTemp("", "mygit.*.blob")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temp file: %s\n", err)
		os.Exit(1)
	}
	defer fileWriter.Close()
	zWriter := zlib.NewWriter(fileWriter)
	defer zWriter.Close()

	writer := io.MultiWriter(hashWriter, zWriter)

	if _, err := io.Copy(writer, reader); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %s\n", err)
		os.Exit(1)
	}
	sha := fmt.Sprintf("%x", hashWriter.Sum(nil))
	prefix, filename := sha[:2], sha[2:]
	if err := os.MkdirAll(path.Join(".git/objects", prefix), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory: %s\n", err)
		os.Exit(1)
	}

	dstFilepath := path.Join(".git/objects", prefix, filename)
	tempFilepath := fileWriter.Name()

	if err := os.Rename(tempFilepath, dstFilepath); err != nil {
		fmt.Fprintf(os.Stderr, "Error renaming file: %s\n", err)
		err = os.Remove(dstFilepath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error removing temp file: %s\n", err)
			os.Exit(1)
		}
		os.Exit(1)
	}
	fmt.Println(sha)
	return 0
}

func lsTree(sha string) int {
	const dir = ".git/objects"
	prefix, filename := sha[:2], sha[2:]
	filepath := path.Join(dir, prefix, filename)
	f, err := os.Open(filepath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening file: %s\n", err)
		os.Exit(1)
	}
	defer f.Close()
	r, err := zlib.NewReader(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading zlib data: %s\n", err)
		os.Exit(1)
	}
	defer r.Close()
	br := bufio.NewReader(r)
	_, err = br.ReadBytes('\x00') // discard header 'tree <length>\x00'
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading header: %s\n", err)
		os.Exit(1)
	}
	for {
		entry, err := nextTreeEntry(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			} else {
				fmt.Fprintf(os.Stderr, "Error reading tree object: %s\n", err)
				os.Exit(1)
			}
		}
		fmt.Println(entry.name)
	}
	return 0
}

func shaData(data []byte) [20]byte {
	return sha1.Sum(data)
}

func writeObject(t string, data []byte) ([20]byte, string) {
	header := fmt.Sprintf("%s %d\x00", t, len(data))
	storeContents := append([]byte(header), data...)
	hashKeyBytes := shaData(storeContents)
	hashKey := hex.EncodeToString(hashKeyBytes[:])
	if len(hashKey) != 40 {
		fmt.Fprintf(os.Stderr, "length hash key=%d invalid\n", len(hashKey))
		os.Exit(1)
	}
	dir := fmt.Sprintf(".git/objects/%s", hashKey[:2])
	filePath := fmt.Sprintf("%s/%s", dir, hashKey[2:])
	if err := os.MkdirAll(string(dir), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s got err=%v\n", string(dir), err)
		os.Exit(1)
	}
	var buf bytes.Buffer
	zWriter := zlib.NewWriter(&buf)
	_, err := zWriter.Write(storeContents)
	if err != nil {
		fmt.Fprintf(os.Stderr, "write zlib to buffer got err=%v\n", err)
		os.Exit(1)
	}
	zWriter.Close()
	err = os.WriteFile(filePath, buf.Bytes(), 0755)
	if err != nil {
		fmt.Fprintf(os.Stderr, "write content to file=%s got err=%v\n", filePath, err)
		os.Exit(1)
	}
	return hashKeyBytes, hashKey
}

func writeTree(path string) ([20]byte, string) {
	dirInfos, err := os.ReadDir(path)
	if err != nil {
		fmt.Printf("Err: %v", err)
		os.Exit(1)
	}
	entries := []string{}
	for _, item := range dirInfos {
		if item.Name() == ".git" {
			continue
		}
		if item.IsDir() {
			hash, _ := writeTree(filepath.Join(path, item.Name()))
			row := fmt.Sprintf("40000 %s\x00%s", item.Name(), hash)
			entries = append(entries, row)
		} else {
			content_file, err := os.ReadFile(filepath.Join(path, item.Name()))
			if err != nil {
				fmt.Printf("Err: %v", err)
				os.Exit(1)
			}
			hashKey, _ := writeObject("blob", content_file)
			row := fmt.Sprintf("100644 %s\x00%s", item.Name(), hashKey)
			entries = append(entries, row)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i][strings.IndexByte(entries[i], ' ')+1:] < entries[j][strings.IndexByte(entries[i], ' ')+1:]
	})
	var buffer bytes.Buffer
	for _, e := range entries {
		buffer.WriteString(e)
	}
	return writeObject("tree", buffer.Bytes())
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>...]\n")
		os.Exit(1)
	}

	switch command := os.Args[1]; command {
	case "init":
		for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating directory: %s\n", err)
			}
		}
		headFileContents := []byte("ref: refs/heads/master\n")
		if err := os.WriteFile(".git/HEAD", headFileContents, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing file: %s\n", err)
		}
		fmt.Println("Initialized git directory")

	case "cat-file":
		option := os.Args[2]
		switch option {
		case "-p":
			blob_sha := os.Args[3]
			fpath := filepath.Join(".git/objects", blob_sha[:2], blob_sha[2:])
			f, err := os.Open(fpath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error opening %s: %s\n", fpath, err)
				os.Exit(1)
			}
			zr, err := zlib.NewReader(f)
			if err != nil {
				fmt.Printf("Err: %v", err)
			}
			defer zr.Close()

			b, _ := io.ReadAll(zr)
			fmt.Print(strings.Split(string(b), "\x00")[1])
		default:
			fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		}

	case "hash-object":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "usage: mygit hash-object -w <path-file>\n")
			os.Exit(1)
		}
		filename := os.Args[3]
		os.Exit(hashObject(filename))

	case "ls-tree":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "usage: mygit hash-object -w <path-file>\n")
			os.Exit(1)
		}
		sha := os.Args[3]
		os.Exit(lsTree(sha))

	case "write-tree":
		_, hash := writeTree(".")
		fmt.Println(hash)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
