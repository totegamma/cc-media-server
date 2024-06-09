FROM golang:latest AS coreBuilder
WORKDIR /work

COPY ./go.mod ./go.sum ./
RUN go mod download && go mod verify
COPY ./ ./
RUN go build -o mediaserver

FROM ubuntu:latest

COPY --from=coreBuilder /work/mediaserver /usr/local/bin

CMD ["mediaserver"]
