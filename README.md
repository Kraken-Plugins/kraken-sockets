# kraken-sockets
A WebSocket server for handling network traffic for the Socket Plugin.

## Deployment

The server now speaks standard WebSockets. Clients should connect to `ws://<host>:26388/` or `wss://<host>:26388/` and send the same JSON packets as before, with `JOIN` still required as the first message.

Deployment is managed through Helm. The included Gateway manifest now uses `HTTPRoute`, so the gateway listener referenced by `sectionName: kraken-sockets` must be configured as an HTTP or HTTPS listener that allows WebSocket upgrades.

Finally build your docker image with: `./scripts/build.sh 0.0.1` and deploy with `./scripts/upgrade.sh` 
