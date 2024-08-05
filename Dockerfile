FROM golang:1.22.5-alpine3.20 AS builder

WORKDIR /app
COPY . /app
ARG IMAGE_TAG
RUN CGO_ENABLED=0 go build --ldflags "-s -w -X github.com/glanceapp/glance/internal/glance.buildVersion=$IMAGE_TAG" .

FROM alpine:3.20

WORKDIR /app
COPY --from=builder /app/glance .

EXPOSE 8080/tcp
ENTRYPOINT ["/app/glance"]
