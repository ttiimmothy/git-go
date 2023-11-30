package main

import (
	"compress/zlib"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

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
			fmt.Fprintf(os.Stderr, "usage: mygit hash-object -w <file>\n")
			os.Exit(1)
		}

		filename := os.Args[3]
		os.Exit(hashObject(filename))

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
