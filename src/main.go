package main

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
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
	ID         string `msgpack:"id"`
	Program    string `msgpack:"program"`
	PhaseIndex int    `msgpack:"phaseIndex"`
	PhaseState string `msgpack:"phaseState"`
}

type TLSResponse struct {
	TrafficLights []TrafficLight `msgpack:"trafficLights"`
}

func StepLoop(wsURL string, steps int) {
	var conn *websocket.Conn
	var err error

	// Endpoints
	tlReq := map[string]string{"endpoint": "trafficlights"}
	stepReq := map[string]string{"endpoint": "step"}
	stopReq := map[string]string{"endpoint": "stop"}

	stepMsg, _ := msgpack.Marshal(stepReq)
	tlMsg, _ := msgpack.Marshal(tlReq)
	stopMsg, _ := msgpack.Marshal(stopReq)

	// Connect to WebSocket
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
		// Send trafficlights request
		if err := conn.WriteMessage(websocket.BinaryMessage, tlMsg); err != nil {
			log.Printf("failed to send trafficlights request: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		_, tlRespBytes, err := conn.ReadMessage()
		if err != nil {
			log.Printf("failed to read TLS response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var tls TLSResponse
		if err := msgpack.Unmarshal(tlRespBytes, &tls); err != nil {
			log.Printf("failed to parse TLS response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Send step request
		if err := conn.WriteMessage(websocket.BinaryMessage, stepMsg); err != nil {
			log.Printf("failed to send step request: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		_, stepRespBytes, err := conn.ReadMessage()
		if err != nil {
			log.Printf("failed to read step response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var stepResp map[string]any
		if err := msgpack.Unmarshal(stepRespBytes, &stepResp); err != nil {
			log.Printf("failed to parse step response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Print
		fmt.Printf("Step %d - TLS:\n", i+1)
		for _, tl := range tls.TrafficLights {
			fmt.Printf("  ID: %s | Program: %s | Phase: %d | State: %s\n",
				tl.ID, tl.Program, tl.PhaseIndex, tl.PhaseState)
		}
	}

	// Send stop request
	if err := conn.WriteMessage(websocket.BinaryMessage, stopMsg); err != nil {
		log.Printf("failed to send stop request: %v", err)
		time.Sleep(500 * time.Millisecond)
	}
}

func main() {
	SetupMiddleware()
	StepLoop("ws://127.0.0.1:5555", 10000)
}
