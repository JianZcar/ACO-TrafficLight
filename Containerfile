FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
ENV GO_VERSION=1.25.1
ENV PATH="/usr/local/go/bin:/opt/pypy-venv/bin:$PATH"

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    wget curl unzip \
    x11vnc xvfb websockify novnc \
    pypy3 pypy3-dev pypy3-venv \
    sumo sumo-tools sumo-doc \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

RUN wget https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz \
    && tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz \
    && rm go${GO_VERSION}.linux-amd64.tar.gz

RUN pypy3 -m venv /opt/pypy-venv
RUN /opt/pypy-venv/bin/pip install --no-cache-dir msgpack sumolib traci

COPY ./src/ ./src/
COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

RUN go build -o ./aco ./src

COPY ./ ./

EXPOSE 8080

CMD ["./bin/start.sh"]

