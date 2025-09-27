package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"
)

func Spinner(message string, done <-chan struct{}) {
	spinner := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	i := 0
	for {
		select {
		case <-done:
			fmt.Printf("\r\033[2K")
			fmt.Println("done " + message)
			return
		default:
			fmt.Printf("\r%s %c", message, spinner[i%len(spinner)])
			i++
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func SetupMiddleware() {
	imageName := "sumo-middleware"

	containerfile, err := filepath.Abs("./src")
	if err != nil {
		log.Fatal(err)
	}

	buildImage := exec.Command("podman", "build", "-t", imageName, containerfile)
	doneBuildImage := make(chan struct{})
	go Spinner("building image", doneBuildImage)

	if err := buildImage.Run(); err != nil {
		close(doneBuildImage)
		time.Sleep(100 * time.Millisecond)
		log.Fatalf("failed to build image: %v", err)
	}
	close(doneBuildImage)
	time.Sleep(100 * time.Millisecond)

	runCmd := exec.Command("podman", "run",
		"--rm",
		"-d",
		"-p", "8080:8080",
		"-p", "5555:5555",
		imageName,
	)
	doneStartContainer := make(chan struct{})
	go Spinner("starting container", doneStartContainer)

	if err := runCmd.Run(); err != nil {
		close(doneStartContainer)
		time.Sleep(100 * time.Millisecond)
		log.Fatalf("failed to run container: %v", err)
	}
	close(doneStartContainer)
	time.Sleep(100 * time.Millisecond)

	fmt.Println("container is running")
	fmt.Println("access GUI at http://localhost:8080/vnc.html?autoconnect=1")
	fmt.Println("middleware available at http://localhost:5555/")
}

type TrafficLight struct {
	ID         string `json:"id"`
	Program    string `json:"program"`
	PhaseIndex int    `json:"phaseIndex"`
	PhaseState string `json:"phaseState"`
}

type TLSResponse struct {
	TrafficLights []TrafficLight `json:"trafficLights"`
}

func StepLoop(middlewareURL string, steps int) {
	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < steps; {
		resp, err := client.Post(
			middlewareURL+"/step",
			"application/json",
			bytes.NewBuffer([]byte(`{}`)),
		)
		if err != nil {
			log.Printf("failed to send step request: %v", err)
			time.Sleep(1 * time.Second)
			continue // don’t increment i
		}
		resp.Body.Close()

		i++

		resp, err = client.Get(middlewareURL + "/trafficlights")
		if err != nil {
			log.Printf("failed to get TLS: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}
		defer resp.Body.Close()

		var tls TLSResponse
		if err := json.NewDecoder(resp.Body).Decode(&tls); err != nil {
			log.Printf("failed to parse TLS response: %v", err)
			continue
		}

		fmt.Printf("Step %d - TLS:\n", i)
		for _, tl := range tls.TrafficLights {
			fmt.Printf("  ID: %s | Program: %s | Phase: %d | State: %s\n",
				tl.ID, tl.Program, tl.PhaseIndex, tl.PhaseState)
		}

		// time.Sleep(1 * time.Second)
	}
}

func main() {
	SetupMiddleware()
	StepLoop("http://127.0.0.1:5555", 1000)
}
