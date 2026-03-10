FROM ubuntu:latest

ENV DEBIAN_FRONTEND=noninteractive
ENV GOLANG_VERSION=1.22.5

RUN apt-get update && apt-get install -y \
  wget \
  ca-certificates \
  git \
  build-essential \
  & rm -rf /var/lib/apt/lists/*

RUN wget https://go.dev/dl/go${GOLANG_VERSION}.linux-amd64.tar.gz \
    && rm -rf /usr/local/go \
    && tar -C /usr/local -xzf go${GOLANG_VERSION}.linux-amd64.tar.gz \
    && rm go${GOLANG_VERSION}.linux-amd64.tar.gz

ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /app

COPY . .

RUN go mod download
RUN go build -o app

CMD ["./app"]
