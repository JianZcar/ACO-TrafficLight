package main

import (
	"fmt"
	"github.com/gorilla/websocket"
	"log"
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

// ----------------------------
// WebSocket Client
// ----------------------------
type TrafficLight struct {
	ID         string `json:"id"`
	Program    string `json:"program"`
	PhaseIndex int    `json:"phaseIndex"`
	PhaseState string `json:"phaseState"`
}

type TLSResponse struct {
	TrafficLights []TrafficLight `json:"trafficLights"`
}

func StepLoop(wsURL string, steps int) {
	var conn *websocket.Conn
	var err error

	for {
		conn, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("failed to connect to WebSocket server: %v, retrying in 1s...", err)
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	defer conn.Close()
	log.Println("connected to WebSocket server")

	for i := 0; i < steps; i++ {
		stepReq := map[string]string{"endpoint": "step"}
		if err := conn.WriteJSON(stepReq); err != nil {
			log.Printf("failed to send step request: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var stepResp map[string]any
		if err := conn.ReadJSON(&stepResp); err != nil {
			log.Printf("failed to read step response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		tlReq := map[string]string{"endpoint": "trafficlights"}
		if err := conn.WriteJSON(tlReq); err != nil {
			log.Printf("failed to send trafficlights request: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var tls TLSResponse
		if err := conn.ReadJSON(&tls); err != nil {
			log.Printf("failed to parse TLS response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		fmt.Printf("Step %d - TLS:\n", i+1)
		for _, tl := range tls.TrafficLights {
			fmt.Printf("  ID: %s | Program: %s | Phase: %d | State: %s\n",
				tl.ID, tl.Program, tl.PhaseIndex, tl.PhaseState)
		}
	}

	stopReq := map[string]string{"endpoint": "stop"}
	if err := conn.WriteJSON(stopReq); err != nil {
		log.Printf("failed to send stop request: %v", err)
		time.Sleep(500 * time.Millisecond)
	}
}

func main() {
	SetupMiddleware()
	StepLoop("ws://127.0.0.1:5555", 1000)
}
