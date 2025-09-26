package main

import (
	"fmt"
	"log"
	"time"
	"os"
	"os/exec"
	"path/filepath"
)

func Spinner(message string, done <-chan struct{}) {
	spinner := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	i := 0
	for {
		select {
		case <-done:
			fmt.Printf("\r\033[2K")
			fmt.Println(message)
			return
		default:
			fmt.Printf("\r%c %s", spinner[i%len(spinner)], message)
			i++
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func main() {
	imageName := "sumo-middleware"

	containerfile, err := filepath.Abs("./src")
	if err != nil {
		log.Fatal(err)
	}

	buildCmd := exec.Command("podman", "build", "-t", imageName, containerfile)
	done := make(chan struct{})
	go Spinner("Building image...", done)

	if err := buildCmd.Run(); err != nil {
		close(done)
		log.Fatalf("failed to build image: %v", err)
	}
	close(done)
	time.Sleep(100 * time.Millisecond)

	fmt.Println("Starting container...")
	runCmd := exec.Command("podman", "run",
		"--rm",
		// "-d",
		"-p", "8080:8080",
		"-p", "5000:5000",
		imageName,
	)

	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr

	if err := runCmd.Run(); err != nil {
		log.Fatalf("failed to run container: %v", err)
	}

	fmt.Println("Container is running")
	fmt.Println("Access GUI at http://localhost:8080/vnc.html?autoconnect=1")
	fmt.Println("Middleware available at http://localhost:5000/")
}
