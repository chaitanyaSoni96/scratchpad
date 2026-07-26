FROM docker.io/library/golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ ./cmd/...

FROM scratch
COPY --from=build /out/scratchpad-web /scratchpad-web
COPY --from=build /out/scratchpad-mcp /scratchpad-mcp
COPY --from=build /out/scratchpad /scratchpad
ENV SCRATCHPAD_ROOT=/data
EXPOSE 8737
CMD ["/scratchpad-web"]
