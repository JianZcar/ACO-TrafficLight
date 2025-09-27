import websockets
import asyncio
import json
import os
import sys
import subprocess
import threading
import traci
from typing import Callable, Dict

SIM_LOCK = threading.Lock()

# ----------------------
# Endpoint registry
# ----------------------
ENDPOINTS: Dict[str, Callable] = {}


def endpoint(func: Callable):
    ENDPOINTS[func.__name__] = func
    return func


# ----------------------
# SUMO setup
# ----------------------
def setup_sumo() -> list:
    os.environ["SUMO_HOME"] = "/usr/share/sumo"
    sys.path.append(os.path.join(os.environ["SUMO_HOME"], "tools"))
    os.environ["DISPLAY"] = ":1"
    sumoBinary = "sumo-gui"  # "sumo" for headless

    # build network
    subprocess.run(
        [
            "netconvert",
            "-n", "sumo_config/nodes.xml",
            "-e", "sumo_config/edges.xml",
            "-x", "sumo_config/connections.xml",
            "-o", "sumo_config/net.xml",
        ],
        check=True,
    )

    return [
        sumoBinary,
        "--window-size", "1920,1080",
        "--window-pos", "0,0",
        "-c", "sumo_config/config.sumocfg",
        "--start",
        "--quit-on-end",
    ]


try:
    traci.start(setup_sumo())
    print("SUMO/TraCI started successfully.")
except Exception as e:
    print("Failed to start SUMO/TraCI:", e)


# ----------------------
# Endpoints
# ----------------------
@endpoint
def step(params=None):
    with SIM_LOCK:
        traci.simulationStep()
        sim_time = traci.simulation.getTime()
    return {"simTime": sim_time}


@endpoint
def trafficlights(params=None):
    tl_ids = traci.trafficlight.getIDList()
    tl_data = []
    for tl in tl_ids:
        try:
            prog = traci.trafficlight.getProgram(tl)
        except Exception:
            prog = None
        try:
            phase_index = traci.trafficlight.getPhase(tl)
        except Exception:
            phase_index = None
        try:
            phase_state = traci.trafficlight.getRedYellowGreenState(tl)
        except Exception:
            phase_state = None
        tl_data.append({
            "id": tl,
            "program": prog,
            "phaseIndex": phase_index,
            "phaseState": phase_state
        })
    return {"trafficLights": tl_data}


@endpoint
def stop(params=None):
    try:
        traci.close()
        print("Stopping server...")
        if server:
            asyncio.create_task(server.close())
        asyncio.get_event_loop().call_later(0.1, sys.exit, 0)
        return {"status": "stopping everything"}
    except Exception as e:
        return {"error": str(e)}


# ----------------------
# WebSocket server
# ----------------------
async def ws_handler(websocket):
    async for message in websocket:
        try:
            req = json.loads(message)
            endpoint_name = req.get("endpoint")
            params = req.get("params", {})
            func = ENDPOINTS.get(endpoint_name)
            if func:
                result = func(params)
            else:
                result = {"error": f"Unknown endpoint: {endpoint_name}"}
        except Exception as e:
            result = {"error": str(e)}

        await websocket.send(json.dumps(result))


async def main():
    global server
    server = await websockets.serve(ws_handler, "0.0.0.0", 5555)
    print("WebSocket SUMO server listening on ws://0.0.0.0:5555")
    await server.wait_closed()


if __name__ == "__main__":
    asyncio.run(main())
