import os
import sys
import time
import traci
import asyncio
import msgpack
import itertools
import subprocess
from typing import Callable, Dict, Optional

# ----------------------
# Configuration
# ----------------------
SOCKET_PATH = "/tmp/sumo_bridge.sock"  # change as needed

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
    sumoBinary = "sumo-gui"  # or "sumo" for headless

    # Build network (adjust paths as needed)
    subprocess.run(
        [
            "netconvert",
            "-n", "./src/sumo_config/nodes.xml",
            "-e", "./src/sumo_config/edges.xml",
            "-x", "./src/sumo_config/connections.xml",
            "-o", "./src/sumo_config/net.xml",
        ],
        check=True,
    )

    # Keep exact command for sumo
    return [
        sumoBinary,
        "--window-size", "1920,1080",
        "--window-pos", "0,0",
        "-c", "./src/sumo_config/config.sumocfg",
        "--start",
        "--quit-on-end",
    ]


# ----------------------
# Cache for traffic lights
# ----------------------
TL_IDS = []
TL_PROGRAMS: Dict[str, str] = {}
TL_CACHE = []


async def init_tl_cache():
    global TL_CACHE
    TL_CACHE = []
    for tl in TL_IDS:
        TL_CACHE.append({
            "id": tl,
            "program": TL_PROGRAMS.get(tl),
            "phaseIndex": 0,
            "phaseState": ""
        })


# ----------------------
# SUMO control
# ----------------------
async def start_sumo():
    # This starts SUMO (blocking call inside async startup).
    # If start is slow it's OK — it happens once at startup.
    traci.start(setup_sumo())


# ----------------------
# Endpoints
# ----------------------
@endpoint
def step(params: Optional[dict] = None):
    start = time.time()
    traci.simulationStep()
    return {"simTime": time.time() - start}


@endpoint
def trafficlights(params: Optional[dict] = None):
    cache = TL_CACHE
    for tl_dict in cache:
        tl = tl_dict["id"]
        new_phase = traci.trafficlight.getPhase(tl)
        if new_phase != tl_dict["phaseIndex"]:
            tl_state = traci.trafficlight.getRedYellowGreenState(tl)
            tl_dict.update({
                "phaseIndex": new_phase,
                "phaseState": tl_state
            })
    return {"trafficLights": cache}


@endpoint
def junction_lanes(params: Optional[dict] = None):
    params = params or {}
    jid = params.get("junction_id") or params.get(
        "junction") or params.get("jid")
    if not jid:
        return {"error": "junction_id required"}
    try:
        incoming_lanes = _get_incoming_lanes(jid)
        incoming_edges = sorted(
            {ln.rsplit("_", 1)[0] for ln in incoming_lanes if "_" in ln})
        lanes_with_links = [_lane_with_links(ln) for ln in incoming_lanes]

        return {
            "junction_id": jid,
            "incoming_edges": incoming_edges,
            "incoming_lanes": lanes_with_links,
        }
    except Exception as e:
        return {"error": str(e)}


def _get_incoming_lanes(jid: str) -> list[str]:
    """Try controlled lanes first, fallback to scanning edges/lanes."""
    try:
        lanes = traci.trafficlight.getControlledLanes(jid) or []
        return list(dict.fromkeys(lanes))
    except Exception:
        pass

    try:
        edges = traci.edge.getIDList()
        all_lanes = traci.lane.getIDList()
    except Exception as e:
        raise RuntimeError(f"traci_error: {e}")

    incoming_edges = [e for e in edges if traci.edge.getToJunction(e) == jid]
    incoming_lanes = itertools.chain.from_iterable(
        (ln for ln in all_lanes if ln.startswith(e + "_")
         ) for e in incoming_edges
    )

    return list(dict.fromkeys(incoming_lanes))


def _lane_with_links(ln: str) -> dict:
    """Return lane and its connected lanes."""
    try:
        links = traci.lane.getLinks(ln) or []
        linked_lanes = [lk[0] for lk in links]
    except Exception:
        linked_lanes = []
    return {"lane": ln, "links_to": linked_lanes}


@endpoint
def evaluate_lane(params: Optional[dict] = None):
    """
    Request:
      {"lane": "w_in"}

    Response:
      {"lane": "w_in", "queue": 3, "wait": 12.0}
    """
    params = params or {}
    lane = params.get("lane")
    if not lane:
        return {"error": "lane required"}

    try:
        q = traci.lane.getLastStepVehicleNumber(lane)
        w = traci.lane.getWaitingTime(lane)
        return {"lane": lane, "queue": int(q), "wait": float(w)}
    except Exception as e:
        return {"lane": lane, "error": str(e)}


@endpoint
async def stop(params: Optional[dict] = None):
    # Ask server to close. The server will actually be closed by calling
    # server.close() on the server object (set in main). Here we close traci
    # and return an immediate confirmation.
    # The main loop will handle server close.
    try:
        traci.close()
    except Exception:
        pass
    # Returning a message; main() should close the server after handling this.
    return {"status": "stopping"}


# ----------------------
# Helpers: length-prefixed framing
# ----------------------
async def read_exact(reader: asyncio.StreamReader, n: int) -> bytes:
    """Read exactly n bytes or raise EOFError."""
    data = await reader.readexactly(n)
    return data


async def read_message(reader: asyncio.StreamReader) -> dict:
    """Read a single length-prefixed msgpack message."""
    # header: 4 bytes big-endian length
    hdr = await read_exact(reader, 4)
    length = int.from_bytes(hdr, "big")
    if length <= 0:
        raise ValueError("Invalid message length")
    body = await read_exact(reader, length)
    return msgpack.unpackb(body, raw=False)


async def send_message(writer: asyncio.StreamWriter, obj: dict):
    payload = msgpack.packb(obj, use_bin_type=True)
    writer.write(len(payload).to_bytes(4, "big") + payload)
    await writer.drain()


# ----------------------
# Client handler
# ----------------------
async def handle_client(reader: asyncio.StreamReader,
                        writer: asyncio.StreamWriter):
    peername = getattr(writer.get_extra_info("peername"),
                       "__str__", lambda: "uds_client")()
    print(f"Client connected: {peername}")
    try:
        while True:
            try:
                req = await read_message(reader)
            except asyncio.IncompleteReadError:
                # client closed connection
                break
            except Exception as e:
                # Send back an error and continue
                err = {"error": f"recv_error: {str(e)}"}
                await send_message(writer, err)
                continue

            # Process request
            try:
                endpoint_name = req.get("endpoint")
                params = req.get("params", {})
                func = ENDPOINTS.get(endpoint_name)
                if func:
                    if asyncio.iscoroutinefunction(func):
                        result = await func(params)
                    else:
                        result = func(params)
                else:
                    result = {"error": "Unknown endpoint"}
            except Exception as e:
                result = {"error": str(e)}

            # Send response
            try:
                await send_message(writer, result)
            except Exception as e:
                print("Failed to send response:", e)
                break

            # If the endpoint was stop — close server after replying
            if req.get("endpoint") == "stop":
                # let main() handle server shutdown; break connection loop
                break

    finally:
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass
        print("Client disconnected")


# ----------------------
# Main
# ----------------------
async def main():
    # ensure old socket removed
    try:
        if os.path.exists(SOCKET_PATH):
            os.remove(SOCKET_PATH)
    except Exception as e:
        print("Warning: couldn't remove existing socket:", e)

    # start SUMO first
    await start_sumo()

    # initialize TL lists/cache
    global TL_IDS, TL_PROGRAMS, TL_CACHE
    TL_IDS = traci.trafficlight.getIDList()
    TL_PROGRAMS = {tl: traci.trafficlight.getProgram(tl) for tl in TL_IDS}
    TL_CACHE = []
    for tl in TL_IDS:
        TL_CACHE.append({
            "id": tl,
            "program": TL_PROGRAMS[tl],
            "phaseIndex": traci.trafficlight.getPhase(tl),
            "phaseState": traci.trafficlight.getRedYellowGreenState(tl)
        })

    # create UDS server
    server = await asyncio.start_unix_server(handle_client, path=SOCKET_PATH)
    print(f"UDS server listening on {SOCKET_PATH}")

    try:
        # Serve forever until stopped (stop endpoint or KeyboardInterrupt)
        await server.serve_forever()
    except asyncio.CancelledError:
        pass
    finally:
        # cleanup
        try:
            server.close()
            await server.wait_closed()
        except Exception:
            pass
        try:
            if os.path.exists(SOCKET_PATH):
                os.remove(SOCKET_PATH)
        except Exception:
            pass
        print("Server shut down")

if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("Server stopped manually")
    except Exception as e:
        print("Fatal error:", e)
        raise
