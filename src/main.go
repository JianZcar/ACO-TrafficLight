package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// ----------------------------
// Msg types
// ----------------------------

type TrafficLightPhase struct {
	State    string  `msgpack:"state"`
	Duration float64 `msgpack:"duration"`
}

type TrafficLightCycle struct {
	ProgramID string              `msgpack:"program"`
	Phases    []TrafficLightPhase `msgpack:"phases"`
}

type TrafficLight struct {
	ID             string            `msgpack:"id"`
	Program        string            `msgpack:"program"`
	PhaseIndex     int               `msgpack:"phaseIndex"`
	PhaseState     string            `msgpack:"phaseState"`
	PhaseRemaining float64           `msgpack:"phaseRemaining"`
	NextSwitch     float64           `msgpack:"nextSwitch"`
	Cycle          TrafficLightCycle `msgpack:"cycle"`
}

type TLSResponse struct {
	TrafficLights []TrafficLight `msgpack:"trafficLights"`
}

type StepResponse struct {
	SimTime float64 `msgpack:"simTime"`
}

type IncomingLane struct {
	Lane    string   `msgpack:"lane"`
	LinksTo []string `msgpack:"links_to"`
}

type JunctionLanesResponse struct {
	JunctionID    string         `msgpack:"junction_id"`
	IncomingEdges []string       `msgpack:"incoming_edges"`
	IncomingLanes []IncomingLane `msgpack:"incoming_lanes"`
}

type EvaluateLaneResponse struct {
	Lane  string  `msgpack:"lane"`
	Queue int     `msgpack:"queue"`
	Wait  float64 `msgpack:"wait"`
}

// ----------------------------
// Framing helpers (4-byte BE len + msgpack body)
// ----------------------------
func sendMsg(conn net.Conn, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func readMsg(conn net.Conn) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(hdr[:])
	if length == 0 {
		return nil, nil
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ----------------------------
// Connect helper
// ----------------------------
func connectUDS(socketPath string) net.Conn {
	for {
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			log.Printf("failed to connect to UDS %s: %v — retrying in 1s...", socketPath, err)
			time.Sleep(1 * time.Second)
			continue
		}
		return conn
	}
}

// ----------------------------
// Step loop (UDS)
// ----------------------------
func StepLoop(socketPath string, steps int) {
	// Requests (msgpack-encoded)
	tlReq := map[string]string{"endpoint": "trafficlights"}
	stepReq := map[string]string{"endpoint": "step"}
	stopReq := map[string]string{"endpoint": "stop"}

	tlMsg, _ := msgpack.Marshal(tlReq)
	stepMsg, _ := msgpack.Marshal(stepReq)
	stopMsg, _ := msgpack.Marshal(stopReq)

	conn := connectUDS(socketPath)
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	log.Println("connected to UDS server")

	for i := 0; i < steps; i++ {
		// --- TrafficLights request timing ---
		startTL := time.Now()
		if err := sendMsg(conn, tlMsg); err != nil {
			log.Printf("failed to send trafficlights request: %v — reconnecting...", err)
			conn.Close()
			conn = connectUDS(socketPath)
			i-- // retry same iteration
			time.Sleep(500 * time.Millisecond)
			continue
		}

		tlRespBytes, err := readMsg(conn)
		if err != nil {
			log.Printf("failed to read TLS response: %v — reconnecting...", err)
			conn.Close()
			conn = connectUDS(socketPath)
			i-- // retry same iteration
			time.Sleep(500 * time.Millisecond)
			continue
		}
		durationTL := time.Since(startTL)

		var tls TLSResponse
		if err := msgpack.Unmarshal(tlRespBytes, &tls); err != nil {
			log.Printf("failed to parse TLS response: %v — retrying iteration...", err)
			i-- // retry same iteration
			time.Sleep(500 * time.Millisecond)
			continue
		}

		fmt.Printf("TrafficLights: \n")
		for _, tl := range tls.TrafficLights {
			fmt.Printf("  ID: %s | Program: %s | Phase: %d | State: %s\n  ⮩ Cycle: ",
				tl.ID, tl.Program, tl.PhaseIndex, tl.PhaseState)

			for i, ph := range tl.Cycle.Phases {
				fmt.Printf("[%d] %s (%.1fs) ", i, ph.State, ph.Duration)
			}
			fmt.Println()
		}

		if len(tls.TrafficLights) == 0 {
		} else {
			firstTL := tls.TrafficLights[0]

			jReq := map[any]any{
				"endpoint": "junction_lanes",
				"params": map[string]any{
					"junction_id": firstTL.ID,
				},
			}
			jMsg, _ := msgpack.Marshal(jReq)

			// send request
			if err := sendMsg(conn, jMsg); err != nil {
				log.Printf("failed to send junction_lanes request for %s: %v", firstTL.ID, err)
				continue
			}

			// read response
			jRespBytes, err := readMsg(conn)
			if err != nil {
				log.Printf("failed to read junction_lanes response for %s: %v", firstTL.ID, err)
				continue
			}

			// unmarshal into typed struct
			var jResp JunctionLanesResponse
			if err := msgpack.Unmarshal(jRespBytes, &jResp); err != nil {
				log.Printf("failed to parse junction_lanes response for %s: %v", firstTL.ID, err)
				continue
			}

			// print summary
			fmt.Printf("Junction lanes for %s: %+v\n", jResp.JunctionID, jResp)

			// loop all incoming lanes and evaluate each one (flat control flow)
			for _, in := range jResp.IncomingLanes {
				laneID := in.Lane

				evalReq := map[any]any{
					"endpoint": "evaluate_lane",
					"params": map[string]any{
						"lane": laneID,
					},
				}
				evalMsg, _ := msgpack.Marshal(evalReq)

				if err := sendMsg(conn, evalMsg); err != nil {
					log.Printf("failed to send evaluate_lane for %s: %v", laneID, err)
					// go to next lane
					continue
				}

				evalRespBytes, err := readMsg(conn)
				if err != nil {
					log.Printf("failed to read evaluate_lane response for %s: %v", laneID, err)
					continue
				}

				var evalResp EvaluateLaneResponse
				if err := msgpack.Unmarshal(evalRespBytes, &evalResp); err != nil {
					log.Printf("failed to parse evaluate_lane response for %s: %v", laneID, err)
					continue
				}

				// print lane evaluation (flat)
				fmt.Printf("    Lane %s | queue=%d wait=%.2f | links_to=%v\n",
					evalResp.Lane, evalResp.Queue, evalResp.Wait, in.LinksTo)
			}
		}

		// --- Step request timing ---
		startStep := time.Now()
		if err := sendMsg(conn, stepMsg); err != nil {
			log.Printf("failed to send step request: %v — reconnecting...", err)
			conn.Close()
			conn = connectUDS(socketPath)
			i-- // retry same iteration
			time.Sleep(500 * time.Millisecond)
			continue
		}

		stepRespBytes, err := readMsg(conn)
		if err != nil {
			log.Printf("failed to read step response: %v — reconnecting...", err)
			conn.Close()
			conn = connectUDS(socketPath)
			i-- // retry same iteration
			time.Sleep(500 * time.Millisecond)
			continue
		}
		durationStep := time.Since(startStep)

		var stepResp StepResponse
		if err := msgpack.Unmarshal(stepRespBytes, &stepResp); err != nil {
			log.Printf("failed to parse step response: %v — retrying iteration...", err)
			i-- // retry same iteration
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// --- Print results (same as before) ---
		fmt.Printf("Sim %.3f ms | Step Request %d %.3f ms | TrafficLights Request %.3f\n",
			stepResp.SimTime*1000, i+1,
			float64(durationStep.Microseconds())/1000,
			float64(durationTL.Microseconds())/1000)

	}

	// send stop
	if conn != nil {
		if err := sendMsg(conn, stopMsg); err != nil {
			log.Printf("failed to send stop request: %v", err)
		}
	}
}

func main() {
	StepLoop("/tmp/sumo_bridge.sock", 10000)
}
