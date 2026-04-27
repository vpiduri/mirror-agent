FROM golang:1.21-alpine AS builder
WORKDIR /src
COPY . .
RUN go build -o /ewp-sim .

FROM alpine:3.19
COPY --from=builder /ewp-sim /ewp-sim
EXPOSE 8081
ENTRYPOINT ["/ewp-sim"]
