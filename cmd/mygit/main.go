package main

import (
	"bufio"
	"compress/zlib"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
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
		filename := os.Args[3]
		os.Exit(hashObject(filename))

	case "ls-tree":
		sha := os.Args[3]
		os.Exit(lsTree(sha))

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
