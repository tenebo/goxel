package goxel

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

//	func RunTor(ready chan bool, torn int) {
//		go execTor(torn)
//		go checkTor(ready)
//	}
func CheckTor()bool {
	wait := true
	for wait {
		fmt.Println("FETCH 9001")
		time.Sleep(time.Second * 3)
		res, err := http.Get("http://127.0.0.1:9000")
		if err != nil {
			fmt.Println("TOR", "Can not fetch proxy: "+err.Error())
			continue
			// cMessages <- NewErrorMessage("TOR", "Can not fetch proxy: "+err.Error())
		}
		if res.StatusCode == 501 {
			return true
		}
	}
	return false
}
func ExecTor(done chan bool, torn int) {
	var stdoutBuf, stderrBuf bytes.Buffer

	torrc, err := makeTorrc(torn)
	if err != nil {
		fmt.Println("TOR", "Can not create torrc file: "+err.Error())
		return
	}
	cmd := exec.Command("tor", "-f", torrc)

	cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	err = cmd.Start() // Starts command asynchronously
	
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	
	defer cmd.Process.Kill()
	<- done
}

func makeTorrc(torn int) (string, error) {
	tmpfolder, err := os.MkdirTemp("", "*")
	if err != nil {
		return "", err
	}
	out := ""
	for i := 0; i < torn; i++ {
		out += fmt.Sprintf("SocksPort %d IsolateDestAddr\n", 9000+i)
	}
	torrcfile := filepath.Join(tmpfolder, "torrc")
	// log.Println(torrcfile)
	err = os.WriteFile(torrcfile, []byte(out), 999)
	if err != nil {
		return "", err
	}
	return torrcfile, nil
}
