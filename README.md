# v2-to-FHIR lab

A local lab for learning HL7 v2 to FHIR R4 conversion, built so each component can be swapped for a home-grown replacement later (Waggle for the engine, Nectar for the FHIR server) and so the same stack can move to a Raspberry Pi cluster or AWS.

```
                 hospital network          integration network
┌──────────────┐        ┌─────────┐  ADT :6661  ┌────────┐  REST  ┌──────┐
│  Simulated   │ MLLP   │         │  ORM :6662  │        │ ─────▶ │      │
│  Hospital    │ ─────▶ │ gateway │  ORU :6663  │ engine │        │ fhir │
│  (hospital)  │ :2575  │         │  MDM :6664  │ (OIE)  │        │(HAPI)│
└──────────────┘        └────┬────┘ ──────────▶ └───┬────┘        └──┬───┘
                             │                      │                │
                     data/archive/            engine-db          fhir-db
                     (golden inputs)          (Postgres)        (Postgres)
```

## Why there's a gateway

Simulated Hospital models a single hospital and pushes every message type down one MLLP connection. Real hospitals don't: registration, order entry, the lab system and transcription each have their own interface. The gateway (a small Go service in `gateway/`) accepts the combined stream, ACKs it, archives every message, and fans it out to one connection per feed.

Each feed has its own queue, so ordering holds within a feed but not across feeds, exactly like production. An ORU can reach the engine before the ADT^A01 for the same patient. That's deliberate; handling it is part of the lesson.

The gateway also writes every message to `data/archive/<feed>/<date>.hl7`. That's the input half of your golden corpus.

## First run

Prerequisites: Docker Desktop (or Docker Engine with the compose plugin), about 4 GB of free RAM.

```sh
docker compose build          # first build of the hospital image takes a few minutes
docker compose up -d
docker compose ps             # everything should be "running"; gateway "healthy"
```

Then check each piece:

- Gateway stats: http://localhost:8081/stats. Messages will show as `queued` with `connected: false` until the engine channels exist. The gateway retries until they do, so nothing is lost.
- Simulated Hospital dashboard: http://localhost:8000/simulated-hospital/
- HAPI: http://localhost:8080/fhir/metadata (give it a minute or two to start; watch `docker compose logs -f fhir`)
- Archive: `ls data/archive/*`

### Engine setup (one time)

1. Open the OIE Administrator (get the launcher from openintegrationengine.org) and connect to `https://localhost:8443`. The default login is admin / admin; change it.
2. Create four channels, each with a **TCP Listener** source in MLLP mode on ports 6661 (ADT), 6662 (ORM), 6663 (ORU) and 6664 (MDM). The data type is HL7 v2.x. For now, leave the destinations empty or send to a Channel Writer; the point is just to accept and ACK.
3. Deploy them and watch the gateway's `delivered` counters climb.

The ADT channel's transformer is where the actual v2-to-FHIR work starts.

## Everyday commands

```sh
docker compose logs -f gateway hospital      # follow specific services
docker compose stop hospital                 # pause message generation
docker compose restart engine
docker compose up -d --build gateway         # rebuild after changing gateway code

# Wipe the FHIR data and start clean (the engine is untouched):
docker compose rm -sf fhir fhir-db && docker volume rm v2fhirlab_fhir-db && docker compose up -d

docker compose down                          # stop everything, keep data
docker compose down -v                       # stop everything and delete ALL volumes
```

## Things to know

**Simulated Hospital version.** The project was archived in March 2025 and its prebuilt image no longer pulls, so `hospital/Dockerfile` builds it from source with Go modules. This is the one piece that couldn't be test-built before you received it. If `docker compose build hospital` fails, the error is almost certainly a dependency version, and it's a quick fix.

**HL7 version.** Simulated Hospital emits v2.3 messages. The v2-to-FHIR IG is written against later versions, but the core segments map the same way, and plenty of production feeds are still 2.3.

**Pathways.** The `.env` defaults run three short scripted pathways in a fixed order (`hospital/pathways/lab_pathways.yml`), so the corpus is reproducible. For random, realistic traffic, set `HOSPITAL_PATHWAY_MANAGER=distribution` and clear `HOSPITAL_PATHWAYS`. Note that the stock pathways use real-time delays of minutes to hours.

**Archive permissions on Linux and Raspberry Pi.** The gateway runs as a non-root user (uid 65532). Docker Desktop on a Mac handles this for you. On Linux, run `sudo chown 65532 data/archive` once.

**Pin your images.** `.env` uses `:latest` for OIE and HAPI to get you started. Once things work, pin them to exact versions so the lab doesn't change underneath you.

## Docker concepts in this repo

- **Multi-stage builds** (`gateway/Dockerfile`, `hospital/Dockerfile`): compile in a full Go image, ship only the binary on distroless. The gateway image is a few MB.
- **Cross-compilation**: `--platform=$BUILDPLATFORM` plus `TARGETARCH` builds arm64 images natively, without emulation.
- **Healthchecks and startup order**: `depends_on: condition: service_healthy`. Simulated Hospital exits if it can't connect at startup, so it waits for the gateway to report healthy. The gateway has no shell or curl, so it health-checks itself with `/gateway -healthcheck`.
- **Networks as security boundaries**: four networks. The hospital can only reach the gateway, and each database is only reachable by its own service.
- **Named volumes vs bind mounts**: databases use named volumes (managed by Docker). Config and the archive use bind mounts (files you edit or read).
- **Compose file layering**: `COMPOSE_FILE` in `.env` merges the base file with one engine file and one FHIR file. Run `docker compose config` to see the merged result.
- **Localhost-only ports**: every published port is bound to `127.0.0.1`, so nothing is exposed to your network.

## Swapping components

Anything that listens on the four feed ports can be the engine. Anything that serves FHIR REST at `http://fhir:8080/fhir` can be the FHIR server. To swap one, add a file like `stack/engine.waggle.yaml` that defines a service named `engine`, then change `COMPOSE_FILE` in `.env`. Nothing upstream needs to change.

## Raspberry Pi

Every image here is available for arm64: the gateway and hospital cross-compile, OIE and Postgres publish arm64 images, and HAPI does too (confirm with `docker manifest inspect hapiproject/hapi:latest`). A single Pi 5 with 8 GB can run the whole stack; the two JVMs are the heavy part.

To spread the stack across several Pis, the service and network layout carries over to Docker Swarm or k3s. Build the images with `docker buildx build --platform linux/arm64,linux/amd64` and push them to a registry the Pis can pull from.

## AWS

The layout maps one-to-one:

- Images go to ECR, and each service becomes an ECS Fargate task.
- `engine-db` and `fhir-db` become RDS Postgres.
- The four networks become subnets and security groups.
- MLLP is plain TCP, so the gateway and engine feeds sit behind a Network Load Balancer, not an Application Load Balancer.
- `data/archive` becomes S3.
- HealthLake can stand in as a third FHIR server to compare against.
