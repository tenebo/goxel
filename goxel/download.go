package goxel

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
)

type download struct {
	Chunk                *Chunk
	OutputPath, InputURL string
	FileID               uint32
}

func teeReaderFunc(d *download, r io.Reader, w io.Writer) io.Reader {
	return &teeReader{d, r, w}
}

type teeReader struct {
	d *download
	r io.Reader
	w io.Writer
}

func (t *teeReader) Read(p []byte) (n int, err error) {
	n, err = t.r.Read(p)
	if n > 0 && t.d.Chunk.Total > t.d.Chunk.Done {
		if n, err := t.w.Write(p[:n]); err != nil {
			return n, err
		}
	}
	return
}

// RebalanceChunks ensures new connections have a chunk attributed to help delayed ones
func RebalanceChunks(h chan header, d chan download, files []*File) {
	closed := false
	for {
		f := <-h

		var file *File
		for _, fi := range files {
			if fi.ID == f.FileID {
				file = fi
				break
			}
		}

		remaining := file.Size
		var idx int
		for i, chunk := range file.Chunks {
			if chunk.Total-chunk.Done > uint64(0.1*float64(file.Size)) && remaining > chunk.Total-chunk.Done {
				remaining = chunk.Total - chunk.Done
				idx = i
			}
		}

		if remaining != file.Size {
			chunk := file.splitChunkInPlace(&file.Chunks[idx], f.ChunkID)
			d <- download{
				Chunk:      chunk,
				InputURL:   file.URL,
				OutputPath: file.Output,
			}
		} else {
			if !closed {
				close(d)
				closed = true
			}
		}
	}
}

// DownloadWorker is the worker functions that processes the download of one Chunk.
// It takes a WaitGroup to ensure all workers have finished before exiting the program.
// It also takes a Channel of Chunks to receive the chunks to download.
func DownloadWorker(i int, wg *sync.WaitGroup, chunks chan download, bs int, finished chan header) {
	defer wg.Done()

	client, err := NewClient()
	if err != nil {
		fmt.Printf(err.Error())
	}

	for {
		download, more := <-chunks
		if !more {
			break
		}

		handleChunkDownload(&download, i, client, bs)

		if len(chunks) == 0 {
			finished <- header{
				FileID:  download.FileID,
				ChunkID: download.Chunk.ID,
			}
		}
	}
}

func handleChunkDownload(download *download, i int, client *http.Client, bs int) {
	activeConnections.inc()
	defer activeConnections.dec()

	chunk := download.Chunk
	chunk.Worker = uint32(i)

	if chunk.Total <= chunk.Done {
		return
	}

	req, err := http.NewRequest("GET", download.InputURL, nil)
	req.Header.Set("Range", "bytes="+strconv.FormatUint(chunk.Start+chunk.Done, 10)+"-"+strconv.FormatUint(chunk.End, 10))

	for name, value := range goxel.Headers {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf(err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode > 399 {
		fmt.Printf("An HTTP error occurred: status %v", resp.StatusCode)
		return
	}

	out, err := os.OpenFile(download.OutputPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		log.Printf(err.Error())
		return
	}
	defer out.Close()

	out.Seek(int64(chunk.Start+chunk.Done), 0)

	var src io.Reader
	src = teeReaderFunc(download, resp.Body, chunk)

	size := bs * 1024
	if l, ok := src.(*io.LimitedReader); ok && int64(size) > l.N {
		if l.N < 1 {
			size = 1
		} else {
			size = int(l.N)
		}
	}
	buf := make([]byte, size)
	_, err = io.CopyBuffer(out, src, buf)
}
