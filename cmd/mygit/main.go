package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type TreeEntry struct {
	mode string
	name string
	sha  string
}

type Object struct {
	Type byte
	Buf  []byte
}

type GitObjectReader struct {
	objectFileReader *bufio.Reader
	ContentSize      int64
	Type             string
	Sha              string
}

type TreeChild struct {
	mode string
	name string
	sha  string
}

type Tree struct {
	children []TreeChild
}

const (
	msbMask      = uint8(0b10000000)
	remMask      = uint8(0b01111111)
	objMask      = uint8(0b01110000)
	firstRemMask = uint8(0b00001111)

	objCommit = 1
	objTree   = 2
	objBlob   = 3

	objOfsDelta = 6
	objRefDelta = 7
)

var (
	shaToObj map[string]Object = make(map[string]Object)
)

func nextTreeEntry(br *bufio.Reader) (TreeEntry, error) {
	modeBytes, err := br.ReadBytes(' ')
	if err != nil {
		return TreeEntry{}, err
	}
	mode := string(modeBytes[:len(modeBytes)-1])
	nameBytes, err := br.ReadBytes('\x00')
	if err != nil {
		return TreeEntry{}, err
	}
	name := string(nameBytes[:len(nameBytes)-1])

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
			contentFile, err := os.ReadFile(filepath.Join(path, item.Name()))
			if err != nil {
				fmt.Printf("Err: %v", err)
				os.Exit(1)
			}
			hashKey, _ := writeObject("blob", contentFile)
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

func commit(treeHash, parentHash, msg string) string {
	sb := strings.Builder{}
	sb.WriteString("tree " + treeHash + "\n")
	sb.WriteString("parent " + parentHash + "\n")
	sb.WriteString("\n" + msg + "\n")

	hashKeyBytes := shaData([]byte(sb.String()))
	hashKey := hex.EncodeToString(hashKeyBytes[:])
	header := fmt.Sprintf("commit %d\x00", sb.Len())
	storeContents := append([]byte(header), []byte(sb.String())...)
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
	pathCommit := filepath.Join(".git", "refs", "heads", "master")
	content := hashKey + "\n"
	err = os.WriteFile(pathCommit, []byte(content), 0755)
	if err != nil {
		fmt.Fprintf(os.Stderr, "write data to commit file=%s got err=%v\n", pathCommit, err)
		os.Exit(1)
	}
	return hashKey
}

func initGitRepository(repoPath string) error {
	for _, dir := range []string{".git", ".git/objects", ".git/refs"} {
		dirPath := path.Join(repoPath, dir)
		if err := os.Mkdir(dirPath, 0755); err != nil && !os.IsExist(err) {
			return err
		}
	}
	headFileContents := []byte("ref: refs/heads/master\n")
	headPath := path.Join(repoPath, ".git/HEAD")
	if err := os.WriteFile(headPath, headFileContents, 0644); err != nil {
		return err
	}
	return nil
}

func readPacketLine(reader io.Reader) ([]byte, error) {
	hex := make([]byte, 4)
	if _, err := reader.Read(hex); err != nil {
		return []byte{}, err
	}

	size, err := strconv.ParseInt(string(hex), 16, 64)
	if err != nil {
		return []byte{}, err
	}
	if size == 0 {
		return []byte{}, nil
	}

	buf := make([]byte, size-4)
	if _, err := reader.Read(buf); err != nil {
		return []byte{}, err
	}

	return buf, nil
}

func fetchLatestCommit(gitUrl string) (string, error) {
	url := fmt.Sprintf("%s/info/refs?service=git-upload-pack", gitUrl)
	res, err := http.Get(url)
	if err != nil {
		return "", err
	}
	buf := bytes.NewBuffer([]byte{})
	if _, err := io.Copy(buf, res.Body); err != nil {
		return "", err
	}
	reader := bufio.NewReader(buf)

	if _, err := readPacketLine(reader); err != nil {
		return "", err
	}

	if _, err := readPacketLine(reader); err != nil {
		return "", err
	}

	head, err := readPacketLine(reader)
	if err != nil {
		return "", err
	}

	split := strings.Split(string(head), " ")
	return split[0], nil
}

func writeBranchRefFile(repoPath string, branch string, commit string) error {
	refPath := path.Join(repoPath, ".git", "refs", "heads", branch)
	if err := os.MkdirAll(path.Dir(refPath), 0750); err != nil && !os.IsExist(err) {
		return err
	}
	refFileContents := []byte(commit)
	if err := os.WriteFile(refPath, refFileContents, 0644); err != nil {
		return err
	}
	return nil
}

// start of fetch object package
func packetLine(rawLine string) string {
	size := len(rawLine) + 4
	return fmt.Sprintf("%04x%s", size, rawLine)
}

func fetchPacketFile(gitUrl, commitSha string) []byte {
	buf := bytes.NewBuffer([]byte{})

	buf.WriteString(packetLine(fmt.Sprintf("want %s no-progress\n", commitSha)))
	buf.WriteString("0000")
	buf.WriteString(packetLine("done\n"))

	uploadPackUrl := fmt.Sprintf("%s/git-upload-pack", gitUrl)
	resp, err := http.Post(uploadPackUrl, "", buf)
	if err != nil {
		fmt.Printf("[Error] Error in git-upload-pack request: %v\n", err)
	}
	result := bytes.NewBuffer([]byte{})
	if _, err := io.Copy(result, resp.Body); err != nil {
		fmt.Printf("err: %v", err)
	}

	packetFileBuf := result.Bytes()[8:]
	return packetFileBuf
}

// start of read object package
func readObjectTypeAndLen(reader *bytes.Reader) (byte, int, error) {
	num := 0
	b, err := reader.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	objType := (b & objMask) >> 4
	num += int(b & firstRemMask)
	if (b & msbMask) == 0 {
		return objType, num, nil
	}
	i := 0
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		num += int(b) << (4 + 7*i)
		if (b & msbMask) == 0 {
			break
		}
		i++
	}

	return objType, num, nil
}

func readSha(reader io.Reader) (string, error) {
	sha := make([]byte, 20)
	if _, err := reader.Read(sha); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha), nil
}

func decompressObject(reader *bytes.Reader) (*bytes.Buffer, error) {
	decompressedReader, err := zlib.NewReader(reader)
	if err != nil {
		return nil, err
	}
	decompressed := bytes.NewBuffer([]byte{})
	if _, err := io.Copy(decompressed, decompressedReader); err != nil {
		return nil, err
	}
	return decompressed, nil
}

func readDeltified(reader *bytes.Buffer, baseObj *Object) (*bytes.Buffer, error) {
	_, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, err
	}

	dstObjLen, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, err
	}

	result := bytes.NewBuffer([]byte{})
	for reader.Len() > 0 {
		firstByte, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}

		if (firstByte & msbMask) == 0 {
			n := int64(firstByte & remMask)
			if _, err := io.CopyN(result, reader, n); err != nil {
				return nil, err
			}
		} else {
			offset := 0
			size := 0
			for i := 0; i < 4; i++ {
				if (firstByte>>i)&1 > 0 {
					b, err := reader.ReadByte()
					if err != nil {
						return nil, err
					}
					offset += int(b) << (i * 8)
				}
			}

			for i := 4; i < 7; i++ {
				if (firstByte>>i)&1 > 0 {
					b, err := reader.ReadByte()
					if err != nil {
						return nil, err
					}
					size += int(b) << ((i - 4) * 8)
				}
			}

			if _, err := result.Write(baseObj.Buf[offset : offset+size]); err != nil {
				return nil, err
			}
		}
	}
	if result.Len() != int(dstObjLen) {
		return nil, fmt.Errorf("invalid deltified buf: expected: %d, but got: %d", dstObjLen, result.Len())
	}
	return result, nil
}

func (o *Object) typeString() (string, error) {
	switch o.Type {
	case objCommit:
		return "commit", nil
	case objTree:
		return "tree", nil
	case objBlob:
		return "blob", nil
	default:
		return "", fmt.Errorf("invalid type: %d", o.Type)
	}
}

func wrapper(contents []byte, objectType string) (*bytes.Buffer, error) {
	outerContents := bytes.NewBuffer([]byte{})
	outerContents.WriteString(fmt.Sprintf("%s %d\x00", objectType, len(contents)))
	if _, err := io.Copy(outerContents, bytes.NewReader(contents)); err != nil {
		return nil, err
	}
	return outerContents, nil
}

func (o *Object) wrappedBuf() ([]byte, error) {
	t, err := o.typeString()
	if err != nil {
		return []byte{}, err
	}
	wrappedBuf, err := wrapper(o.Buf, t)
	if err != nil {
		return []byte{}, err
	}
	return wrappedBuf.Bytes(), nil
}

func (o *Object) sha() (string, error) {
	b, err := o.wrappedBuf()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha1.Sum(b)), nil
}

func saveObject(o *Object) error {
	objSha, err := o.sha()
	if err != nil {
		return err
	}
	shaToObj[objSha] = *o
	return nil
}

func readObject(reader *bytes.Reader) error {
	objType, objLen, err := readObjectTypeAndLen(reader)
	if err != nil {
		return err
	}
	if objType == objRefDelta {
		baseObjSha, err := readSha(reader)
		if err != nil {
			return err
		}
		baseObj, ok := shaToObj[baseObjSha]
		if !ok {
			return fmt.Errorf("unknown obj sha: %s", baseObjSha)
		}
		decompressed, err := decompressObject(reader)
		if err != nil {
			return err
		}
		deltified, err := readDeltified(decompressed, &baseObj)
		if err != nil {
			return err
		}
		obj := Object{
			Type: baseObj.Type,
			Buf:  deltified.Bytes(),
		}
		if err := saveObject(&obj); err != nil {
			return err
		}
	} else if objType == objOfsDelta {
		return errors.New("Unsupported")
	} else {
		decompressed, err := decompressObject(reader)
		if err != nil {
			return err
		}
		if objLen != decompressed.Len() {
			return fmt.Errorf("expect object length: %d, but get: %d", objLen, decompressed.Len())
		}
		obj := Object{
			Type: objType,
			Buf:  decompressed.Bytes(),
		}
		if err := saveObject(&obj); err != nil {
			return err
		}
	}
	return nil
}

// end of read object package

func fetchObjects(gitRepositoryUrl, commitSha string) error {
	packetFileBuffer := fetchPacketFile(gitRepositoryUrl, commitSha)
	checksumLen := 20
	calculatedChecksum := packetFileBuffer[len(packetFileBuffer)-checksumLen:]
	storedChecksum := sha1.Sum(packetFileBuffer[:len(packetFileBuffer)-checksumLen])
	if !bytes.Equal(storedChecksum[:], calculatedChecksum) {
		fmt.Printf("[Error] expected checksum: %v, but got: %v", storedChecksum, calculatedChecksum)
	}

	headerLen := 12
	bufReader := bytes.NewReader(packetFileBuffer[headerLen:])
	for {
		err := readObject(bufReader)
		if err != nil {
			return err
		}
		if bufReader.Len() <= checksumLen {
			fmt.Printf("[Debug] remaining buf len: %d\n", bufReader.Len())
			break
		}
	}
	return nil
}

// end of fetch object package

func writeGitObject(repoPath string, object []byte) (string, error) {
	blobSha := fmt.Sprintf("%x", sha1.Sum(object))
	objectFilePath := path.Join(repoPath, ".git", "objects", blobSha[:2], blobSha[2:])
	if err := os.MkdirAll(path.Dir(objectFilePath), 0755); err != nil {
		return "", err
	}
	objectFile, err := os.Create(objectFilePath)
	if err != nil {
		return "", err
	}
	compressedFileWriter := zlib.NewWriter(objectFile)
	if _, err = compressedFileWriter.Write(object); err != nil {
		return "", err
	}
	if err := compressedFileWriter.Close(); err != nil {
		return "", err
	}
	return blobSha, nil
}

func writeFetchedObjects(repoPath string) error {
	for _, object := range shaToObj {
		b, err := object.wrappedBuf()
		if err != nil {
			return err
		}
		if _, err := writeGitObject(repoPath, b); err != nil {
			return err
		}
	}
	return nil
}

// start of restore repository package
func NewGitObjectReader(repoPath, objectSha string) (GitObjectReader, error) {
	objectFilePath := path.Join(repoPath, ".git", "objects", objectSha[:2], objectSha[2:])
	objectFile, err := os.Open(objectFilePath)
	if err != nil {
		return GitObjectReader{}, err
	}
	objectFileDecompressed, err := zlib.NewReader(objectFile)
	if err != nil {
		return GitObjectReader{}, err
	}
	objectFileReader := bufio.NewReader(objectFileDecompressed)

	objectType, err := objectFileReader.ReadString(' ')
	if err != nil {
		return GitObjectReader{}, err
	}
	objectType = objectType[:len(objectType)-1]

	objectSizeStr, err := objectFileReader.ReadString(0)
	if err != nil {
		return GitObjectReader{}, err
	}

	objectSizeStr = objectSizeStr[:len(objectSizeStr)-1]
	size, err := strconv.ParseInt(objectSizeStr, 10, 64)
	if err != nil {
		return GitObjectReader{}, err
	}

	return GitObjectReader{
		objectFileReader: objectFileReader,
		Type:             objectType,
		Sha:              objectSha,
		ContentSize:      size,
	}, nil
}
func (g *GitObjectReader) ReadContents() ([]byte, error) {
	contents := make([]byte, g.ContentSize)
	if _, err := io.ReadFull(g.objectFileReader, contents); err != nil {
		return []byte{}, err
	}
	return contents, nil
}

func readObjectContent(repoPath, objSha string) ([]byte, error) {
	objReader, err := NewGitObjectReader(repoPath, objSha)
	if err != nil {
		return []byte{}, err
	}
	contents, err := objReader.ReadContents()
	if err != nil {
		return []byte{}, err
	}
	return contents, nil
}

func parseTree(treeBuf []byte) (*Tree, error) {
	children := make([]TreeChild, 0)
	contentsReader := bufio.NewReader(bytes.NewReader(treeBuf))
	for {
		mode, err := contentsReader.ReadString(' ')
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		mode = mode[:len(mode)-1]
		entryName, err := contentsReader.ReadString(0)
		if err != nil {
			return nil, err
		}
		entryName = entryName[:len(entryName)-1]
		sha := make([]byte, 20)
		_, err = contentsReader.Read(sha)
		if err != nil {
			return nil, err
		}
		children = append(children, TreeChild{
			name: entryName,
			mode: mode,
			sha:  fmt.Sprintf("%x", sha),
		})
	}
	tree := Tree{
		children: children,
	}
	return &tree, nil
}

func getPerm(mode string) (os.FileMode, error) {
	if !strings.HasPrefix(mode, "100") {
		return 0, fmt.Errorf("invalid mode: %s", mode)
	}
	perm, err := strconv.ParseInt(mode[3:], 8, 64)
	if err != nil {
		return 0, err
	}
	return os.FileMode(perm), nil
}

func traverseTree(repoPath, curDir, treeSha string) error {
	treeBuf, err := readObjectContent(repoPath, treeSha)
	if err != nil {
		return err
	}
	tree, err := parseTree(treeBuf)
	if err != nil {
		return err
	}
	for _, child := range tree.children {
		if strings.HasPrefix(child.mode, "100") {
			blobBuf, err := readObjectContent(repoPath, child.sha)
			if err != nil {
				return err
			}
			filePath := path.Join(repoPath, curDir, child.name)
			if err := os.MkdirAll(path.Dir(filePath), 0750); err != nil && !os.IsExist(err) {
				return err
			}
			perm, err := getPerm(child.mode)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filePath, blobBuf, perm); err != nil {
				return err
			}
		} else {
			childDir := path.Join(curDir, child.name)
			if err := traverseTree(repoPath, childDir, child.sha); err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreRepository(repoPath, commitSha string) error {
	commitBuf, err := readObjectContent(repoPath, commitSha)
	if err != nil {
		return err
	}
	commitReader := bufio.NewReader(bytes.NewReader(commitBuf))
	treePrefix, err := commitReader.ReadString(' ')
	if err != nil {
		return err
	}
	if treePrefix != "tree " {
		return fmt.Errorf("invalid commit blob: %s", string(commitBuf))
	}
	treeSha, err := commitReader.ReadString('\n')
	if err != nil {
		return err
	}
	treeSha = treeSha[:len(treeSha)-1]
	if err := traverseTree(repoPath, "", treeSha); err != nil {
		return err
	}
	return nil
}

// end of restore repository package

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mygit <command> [<args>...]\n")
		os.Exit(1)
	}

	switch command := os.Args[1]; command {
	case "init":
		for _, dir := range []string{".git", ".git/objects", ".git/refs", ".git/refs/heads"} {
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
			blobSha := os.Args[3]
			fpath := filepath.Join(".git/objects", blobSha[:2], blobSha[2:])
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

	case "commit-tree":
		if len(os.Args) < 7 {
			fmt.Fprintf(os.Stderr, "usage: mygit commit-tree <tree_sha> -p <commit_sha> -m <message>\n")
			os.Exit(1)
		}
		treeHash := os.Args[2]
		parentSha := os.Args[4]
		msg := os.Args[6]
		hashCommit := commit(treeHash, parentSha, msg)
		fmt.Println(hashCommit)

	case "clone":
		optsClone := os.Args[1]
		if optsClone != "clone" {
			fmt.Printf("Invalid argument: %v\n", os.Args[1:])
			os.Exit(1)
		}

		gitUrl := os.Args[2]
		dir := os.Args[3]
		repoPath := path.Join(".", dir)

		if err := os.MkdirAll(repoPath, 0750); err != nil {
			fmt.Printf("Err: %v", err)
			os.Exit(1)
		}

		if err := initGitRepository(repoPath); err != nil {
			fmt.Printf("Err: %v", err)
			os.Exit(1)
		}

		commitSha, err := fetchLatestCommit(gitUrl)
		if err != nil {
			fmt.Printf("Err: %v", err)
			os.Exit(1)
		}

		if err := writeBranchRefFile(repoPath, "master", commitSha); err != nil {
			fmt.Printf("Err: %v", err)
			os.Exit(1)
		}

		if err := fetchObjects(gitUrl, commitSha); err != nil {
			fmt.Printf("Err: %v", err)
			os.Exit(1)
		}

		if err := writeFetchedObjects(repoPath); err != nil {
			fmt.Printf("Err: %v", err)
			os.Exit(1)
		}

		if err := restoreRepository(repoPath, commitSha); err != nil {
			fmt.Printf("Err: %v", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command %s\n", command)
		os.Exit(1)
	}
}
