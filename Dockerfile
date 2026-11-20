# syntax=docker/dockerfile:1
FROM golang:1.27.4-alpine AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

FROM source AS build
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

FROM source AS test
RUN apk add --no-cache build-base
COPY integration ./integration
COPY migrations ./migrations
ENV CGO_ENABLED=1
CMD ["go", "test", "-race", "-count=1", "./..."]

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates
COPY --from=build /out/api /app/api
COPY --from=build /out/worker /app/worker
USER 65534:65534
EXPOSE 8080
CMD ["/app/api"]

