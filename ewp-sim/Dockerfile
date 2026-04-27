FROM golang:1.21-alpine AS builder
WORKDIR /src
COPY ewp-sim/main.go .
RUN printf 'module ewp-sim\ngo 1.21\n' > go.mod && \
    CGO_ENABLED=0 GOOS=linux go build -o /ewp-sim .

FROM alpine:3.19
COPY --from=builder /ewp-sim /ewp-sim
EXPOSE 9091
ENTRYPOINT ["/ewp-sim"]
