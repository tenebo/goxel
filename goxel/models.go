package goxel

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

const (
	workExtension = "gx"
	maxUint32     = ^uint32(0)
)

// MessageType identifies the severity of the message
type MessageType int

// Message Severities
const (
	Info MessageType = iota + 1
	Warning
	Error
)

func (t MessageType) String() string {
	switch t {
	case 1:
		return "INFO"
	case 2:
		return "WARNING"
	case 3:
		return "ERROR"
	}
	return "UNKNOW"
}

// Message contains global messages to be displayed by monitoring
type Message struct {
	FileID           uint32
	Content, Context string
	Type             MessageType
}

// NewErrorMessage builds an Error message with no related file
func NewErrorMessage(context string, content string) Message {
	return NewMessageForFile(maxUint32, context, content, Error)
}

// NewErrorMessageForFile builds an Error message with a related file
func NewErrorMessageForFile(fileID uint32, context string, content string) Message {
	return NewMessageForFile(fileID, context, content, Error)
}

// NewInfoMessage builds an Info message with no related file
func NewInfoMessage(context string, content string) Message {
	return NewMessageForFile(maxUint32, context, content, Info)
}

// NewInfoMessageForFile builds an Info message with a related file
func NewInfoMessageForFile(fileID uint32, context string, content string) Message {
	return NewMessageForFile(fileID, context, content, Info)
}

// NewWarningMessage builds a Warning message with no related file
func NewWarningMessage(context string, content string) Message {
	return NewMessageForFile(maxUint32, context, content, Warning)
}

// NewWarningMessageForFile builds a Warning message with a related file
func NewWarningMessageForFile(fileID uint32, context string, content string) Message {
	return NewMessageForFile(fileID, context, content, Warning)
}

// NewMessageForFile builds a standard Message
func NewMessageForFile(fileID uint32, context string, content string, t MessageType) Message {
	return Message{
		FileID:  fileID,
		Content: content,
		Context: context,
		Type:    t,
	}
}

// Chunk stores a part of a file being downloaded
type Chunk struct {
	ID, Worker              uint32
	Start, End, Done, Total uint64
}

func (c *Chunk) Write(b []byte) (int, error) {
	n := len(b)
	c.Done += uint64(n)
	return n, nil
}

// BuildProgress builds the progress display for a specific Chunk
// "-" means downloaded during this process
// " " means not yet downloaded
func (c *Chunk) BuildProgress(buf []string, unit float64) {
	if c.End <= c.Start {
		return
	}

	rng := int(float64(c.End-c.Start)*unit) + 1
	offset := int(float64(c.Start) * unit)

	for j := 0; j < int(math.Min(float64(c.Done)*unit, float64(rng)-1)); j++ {
		buf[offset+j] = "-"
	}

	if offset+int(math.Max(math.Min(float64(c.Done)*unit, float64(rng)-1), 0)) >= len(buf) {
		// Progress is already done, do not add worker ID
		return
	}

	buf[offset+int(math.Max(math.Min(float64(c.Done)*unit, float64(rng)-1), 0))] = fmt.Sprintf("%d", c.Worker)

	for j := int(math.Max(math.Min(float64(c.Done)*unit, float64(rng)-1), 0)) + 1; j < rng; j++ {
		if offset+j >= len(buf) {
			break
		}
		buf[offset+j] = " "
	}
}

// File stores a file to be downloaded
type File struct {
	URL, Output, OutputWork      string
	Chunks                       []Chunk
	Finished, Valid, Initialized bool
	Error                        string
	Size, Initial                uint64
	Progress                     []string
	Mux                          sync.Mutex
	ID                           uint32
}

type header struct {
	FileID, ChunkID uint32
}

func (f *File) setOutput(directory string, OverwriteOutputFile bool) {
	if directory != "" {
		err := os.MkdirAll(directory, 0755)
		if err != nil {
			fmt.Printf("[ERROR] Can't create directory [%v]\n", directory)
			os.Exit(1)
		}

		f.Output = path.Join(directory, path.Base(f.URL))
	} else {
		f.Output = path.Base(f.URL)
	}

	initialOutput := f.Output

	idx := -1
	for {
		_, err := os.Stat(f.Output)
		_, errw := os.Stat(f.Output + "." + workExtension)

		if !os.IsNotExist(err) && os.IsNotExist(errw) {
			if idx == -1 && OverwriteOutputFile {
				break
			} else {
				idx++
				f.Output = initialOutput + "." + strconv.Itoa(idx)
			}
		} else {
			break
		}
	}

	f.OutputWork = f.Output + "." + workExtension
}

// BuildProgress builds the progress display for a specific File
// "-" means downloaded during this process
// " " means not yet downloaded
// "+" means already downloaded during a previous process (resumed)
func (f *File) BuildProgress(unit float64) string {
	rng := int(float64(f.Size) * unit)

	if !f.Initialized {
		f.Progress = make([]string, rng)
		for i := 0; i < rng; i++ {
			f.Progress[i] = "+"
		}
		f.Initialized = true
	}

	for _, chunk := range f.Chunks {
		chunk.BuildProgress(f.Progress, unit)
	}

	return strings.Join(f.Progress, "")
}

func (f *File) writeMetadata() {
	if f.Finished {
		return
	}

	f.Valid = true

	if !goxel.Resume {
		return
	}

	file, err := os.OpenFile(f.OutputWork, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Println(err.Error())
		return
	}
	defer file.Close()

	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint64(len(f.Chunks)))

	for _, chunk := range f.Chunks {
		binary.Write(&buf, binary.BigEndian, chunk)
	}

	_, err = file.Write(buf.Bytes())
	if err != nil {
		log.Printf(err.Error())
	}
}

func (f *File) finish() {
	if f.Finished || f.Error != "" {
		return
	}
	f.Finished = true

	_ = os.Remove(f.OutputWork)
}

func (f *File) splitChunkInPlace(baseChunk *Chunk, id uint32) *Chunk {
	f.Mux.Lock()
	defer f.Mux.Unlock()

	remainingPerChunk := (baseChunk.End - baseChunk.Start - baseChunk.Done) / 2

	for i, chunk := range f.Chunks {
		if chunk.ID != uint32(id) {
			continue
		}

		chunk2 := Chunk{
			Start:  baseChunk.End - remainingPerChunk + 1,
			End:    baseChunk.End,
			Worker: 0,
			Done:   0,
			Total:  remainingPerChunk,
			ID:     chunk.ID,
		}
		f.Chunks[i] = chunk2

		baseChunk.End -= remainingPerChunk
		baseChunk.Total = baseChunk.End - baseChunk.Start

		return &f.Chunks[i]
	}
	return nil
}

func (f *File) splitChunk(baseChunk *Chunk) Chunk {
	f.Mux.Lock()
	defer f.Mux.Unlock()

	remainingPerChunk := (baseChunk.End - baseChunk.Start - baseChunk.Done) / 2

	chunk2 := Chunk{
		Start:  baseChunk.End - remainingPerChunk + 1,
		End:    baseChunk.End,
		Worker: 0,
		Done:   0,
		Total:  remainingPerChunk,
	}

	baseChunk.End -= remainingPerChunk
	baseChunk.Total = baseChunk.End - baseChunk.Start + 1

	return chunk2
}

// UpdateStatus returns the current status of the download
// The first returned value is the progress percentage
// The second returned value is the number of active connections for this file
// The third returned value is the number of bytes downloaded
// The last returned value is the number of bytes downloaded during this session
func (f *File) UpdateStatus(commit bool) (float64, uint64, uint64, uint64) {
	var remaining, total, conn uint64
	for i := 0; i < len(f.Chunks); i++ {
		v := f.Chunks[i]
		remaining += v.End - v.Start - v.Done
		total += v.Total

		if v.Done < v.Total && v.Done > 0 {
			conn++
		}
	}

	done := f.Size - remaining
	if !f.Finished {
		if done >= f.Size && done > 0 {
			f.finish()
		}

		if done > 0 && f.OutputWork != "" && commit {
			f.writeMetadata()
		}
	}

	var ratio float64
	if f.Size > 0 {
		ratio = math.Min(float64(done)/float64(f.Size)*100, 100.0)
	}

	return ratio, conn, done, total - remaining
}

// ResumeChunks tries to resume the current download by checking if the file exists and is valid
func (f *File) ResumeChunks(maxConnPerFile int) bool {
	if !goxel.Resume {
		return false
	}

	if _, err := os.Stat(f.OutputWork); !os.IsNotExist(err) {
		var initial []Chunk

		file, err := os.Open(f.OutputWork)
		if err != nil {
			log.Println(err.Error())
			return false
		}
		defer file.Close()

		// Read initial number of chunks
		rbytes := make([]byte, 8)
		_, err = file.Read(rbytes)
		if err != nil {
			return false
		}

		var initialSize uint64
		buf := bytes.NewBuffer(rbytes)
		err = binary.Read(buf, binary.BigEndian, &initialSize)
		if err != nil {
			return false
		}

		initial = make([]Chunk, initialSize)
		for i := 0; uint64(i) < initialSize; i++ {
			rbytes = make([]byte, unsafe.Sizeof(Chunk{}))
			_, err := file.Read(rbytes)
			if err != nil {
				return false
			}

			initial[i] = Chunk{}
			buf := bytes.NewBuffer(rbytes)
			err = binary.Read(buf, binary.BigEndian, &initial[i])
			if err != nil {
				return false
			}
		}

		sort.SliceStable(initial, func(i, j int) bool {
			return initial[i].Start < initial[j].Start
		})

		f.Chunks = make([]Chunk, len(initial))
		for i := 0; i < len(initial); i++ {
			f.Initial += initial[i].Done

			f.Chunks[i] = Chunk{
				Start:  initial[i].Start + initial[i].Done,
				End:    initial[i].End,
				Worker: uint32(i),
				Done:   0,
				Total:  initial[i].Total - initial[i].Done,
			}
		}

		// Re-arrange depending on max-conn-file input
		// Only adding connections is supported
		if maxConnPerFile > len(f.Chunks) {
			sort.SliceStable(f.Chunks, func(i, j int) bool {
				return f.Chunks[i].Total > f.Chunks[j].Total
			})

			for diff := maxConnPerFile - len(f.Chunks); diff > 0; diff-- {
				e2 := f.splitChunk(&f.Chunks[0])

				f.Chunks = append(f.Chunks, e2)

				sort.SliceStable(f.Chunks, func(i, j int) bool {
					return f.Chunks[i].Total > f.Chunks[j].Total
				})
			}
		}

		return true
	}

	return false
}

// BuildChunks builds the Chunks slice for each part of the file to be downloaded
// It retrieves existing metadata file in order to resume downloads.
// Each created chunk is sent to the channel past in parameters.
// The nbrPerFile parameter determines the max number of splits for each file. In case the download
// is being resumed, the nbrPerFile is ignored in favor of the number stored in the metadata file.
func (f *File) BuildChunks(wg *sync.WaitGroup, chunks chan download, nbrPerFile int) {
	defer wg.Done()

	client, err := NewClient()
	if err != nil {
		fmt.Printf(err.Error())
		return
	}

	req, err := http.NewRequest("HEAD", f.URL, nil)
	if err != nil {
		f.Error = fmt.Sprintf("An error occurred: %v", err.Error())
		return
	}

	for name, value := range goxel.Headers {
		req.Header.Set(name, value)
	}

	head, err := client[0].Do(req)
	if err != nil {
		f.Error = fmt.Sprintf("An error occurred: %v", err.Error())
		return
	}
	defer head.Body.Close()

	if head.StatusCode > 399 {
		f.Error = fmt.Sprintf("An HTTP error occurred: status %v", head.StatusCode)
		return
	}

	acceptRanges, acceptRangesOk := head.Header["Accept-Ranges"]

	rawContentLength, ok := head.Header["Content-Length"]
	if !ok || len(rawContentLength) == 0 {
		f.Error = fmt.Sprintf("Can't retrieve file size for file")
		return
	}
	contentLength, _ := strconv.ParseUint(rawContentLength[0], 10, 64)
	f.Size = contentLength

	if resume := f.ResumeChunks(nbrPerFile); !resume {
		if !acceptRangesOk || len(acceptRanges) == 0 || acceptRanges[0] != "bytes" {
			f.Chunks = make([]Chunk, 1)

			f.Chunks[0] = Chunk{
				Start: 0,
				Done:  0,
				End:   contentLength,
				Total: contentLength,
			}
		} else {
			buildRootChunks(f, nbrPerFile)
		}
	}
	f.writeMetadata()

	for i := 0; i < len(f.Chunks); i++ {
		f.Chunks[i].ID = uint32(i)
		chunks <- download{
			Chunk:      &f.Chunks[i],
			InputURL:   f.URL,
			OutputPath: f.Output,
			FileID:     f.ID,
		}
	}
}

func buildRootChunks(f *File, nbrPerFile int) {
	f.Chunks = make([]Chunk, nbrPerFile)

	chunkSize := f.Size / uint64(len(f.Chunks))
	remaining := f.Size - chunkSize*uint64(len(f.Chunks))

	for i := 0; i < len(f.Chunks); i++ {
		f.Chunks[i] = Chunk{
			Start: uint64(i) * chunkSize,
			End: uint64(math.Min(float64(uint64(i+1)*chunkSize-1),
				float64(f.Size))),
			Worker: uint32(i),
			Done:   0,
		}
		f.Chunks[i].Total = f.Chunks[i].End - f.Chunks[i].Start + 1

		if i == len(f.Chunks)-1 {
			f.Chunks[i].End += remaining
		}
	}
}
