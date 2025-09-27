import os
import sys
import traci
import uvloop
import asyncio
import msgpack
import subprocess
import websockets
from typing import Callable, Dict

server = None

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

    # Build network
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

    # Keep your exact command
    return [
        sumoBinary,
        "--window-size", "1920,1080",
        "--window-pos", "0,0",
        "-c", "sumo_config/config.sumocfg",
        "--start",
        "--quit-on-end",
    ]


# ----------------------
# SUMO control
# ----------------------
async def start_sumo():
    traci.start(setup_sumo())


# ----------------------
# Endpoints
# ----------------------
@endpoint
async def step(params=None):
    traci.simulationStep()
    sim_time = traci.simulation.getTime()
    return {"simTime": sim_time}


# Cache
TL_IDS = []
TL_PROGRAMS = []


@endpoint
async def trafficlights(params=None):
    return {
        "trafficLights": [
            {
                "id": tl,
                "program": TL_PROGRAMS[tl],
                "phaseIndex": traci.trafficlight.getPhase(tl),
                "phaseState": traci.trafficlight.getRedYellowGreenState(tl),
            }
            for tl in TL_IDS
        ]
    }


@endpoint
async def stop(params=None):
    traci.close()
    if server:
        await server.close()
    asyncio.get_event_loop().call_later(0.1, sys.exit, 0)
    return {"status": "stopping everything"}


# ----------------------
# WebSocket server
# ----------------------
async def ws_handler(websocket):
    async for message in websocket:
        try:
            req = msgpack.unpackb(message, raw=False)
            func = ENDPOINTS.get(req.get("endpoint"))
            params = req.get("params", {})
            if func:
                result = await func(params)
            else:
                result = {"error": "Unknown endpoint"}
        except Exception as e:
            result = {"error": str(e)}

        await websocket.send(msgpack.packb(result, use_bin_type=True))


# ----------------------
# Main
# ----------------------
async def main():
    global server
    await start_sumo()

    global TL_IDS
    global TL_PROGRAMS

    TL_IDS = traci.trafficlight.getIDList()
    TL_PROGRAMS = {tl: traci.trafficlight.getProgram(tl) for tl in TL_IDS}

    server = await websockets.serve(ws_handler, "0.0.0.0", 5555)
    await server.wait_closed()

if __name__ == "__main__":
    uvloop.install()
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("Server stopped manually")
