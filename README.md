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
- OIE web administrator: https://localhost:8443/oie-webadmin/ (give the engine a minute to start)

### Engine setup (one time)

1. Open the web administrator at https://localhost:8443/oie-webadmin/ (accept the self-signed certificate). The default login is admin / admin; the first login asks you to set a new password. The desktop Administrator launcher from openintegrationengine.org still works too, pointed at `https://localhost:8443`.
2. Import and deploy the ADT channel: `OIE_PASSWORD=<your new password> engine/oie/channels/import.sh`. Or, in the web administrator, use **Channels → Import** on `engine/oie/channels/adt-to-fhir.xml`, then deploy it.
3. The ORM, ORU and MDM feeds don't have channels yet. Until they do, the gateway keeps those messages queued (nothing is lost). To just accept and ACK them for now, create a channel for each with a **TCP Listener** source in MLLP mode on 6662 (ORM), 6663 (ORU) or 6664 (MDM), data type HL7 v2.x, and deploy it.
4. Watch the gateway's `delivered` counters climb, and new Patients and Encounters appear at http://localhost:8080/fhir/Patient.

## ADT to FHIR

The **ADT to FHIR** channel (`engine/oie/channels/adt-to-fhir.xml`) listens on 6661 and turns each **ADT^A01** (admit) into a FHIR transaction Bundle that it posts to `http://fhir:8080/fhir`:

| v2 | FHIR | Notes |
|---|---|---|
| PID-3 (all repetitions) | `Patient.identifier` | CX.4 (assigning authority) picks the `system`; CX.5 becomes the type (`MRN` → v2-0203 `MR`) |
| PID-5 | `Patient.name` | family, given (XPN.2 and .3), prefix, suffix |
| PID-7 | `Patient.birthDate` | date only |
| PID-8 | `Patient.gender` | v2 table 0001 → AdministrativeGender |
| PID-11 | `Patient.address` | |
| PID-13 | `Patient.telecom` | |
| PV1-19 | `Encounter.identifier` | type `VN` |
| (event A01) | `Encounter.status` = `in-progress` | |
| PV1-2 | `Encounter.class` | I → IMP, O → AMB, E → EMER, P → PRENC |
| PV1-3 | `Encounter.location` | ward, room, bed as a display name for now |
| PV1-7 | `Encounter.participant` (ATND) | by identifier and display name for now |
| PV1-10 | `Encounter.serviceType` | v2 table 0069 (MED, SUR, CAR, ...) |
| PV1-44 | `Encounter.period.start` | |

How it behaves:

- **Replays don't duplicate.** Both resources are written with conditional updates (`PUT Patient?identifier=<MRN>`, `PUT Encounter?identifier=<visit number>`), so re-sending a message updates what's there. The Encounter points at the Patient through the bundle, so HAPI links them in one transaction.
- **Time zones.** v2 timestamps carry no offset, but FHIR needs one on any dateTime with a time. The engine gets `SOURCE_TIMEZONE` from `HOSPITAL_TIMEZONE` in `.env`, and the channel reads timestamps in that zone.
- **ACKs mean something.** The channel ACKs only after HAPI answers: AA when the bundle was stored, AE when anything failed (a mapping error or a FHIR error). The gateway counts AE as `rejected`, and the message and the error are in the channel's message browser. Other ADT events are ACKed AA with "not converted to FHIR yet".
- **Simulated Hospital quirks.** It writes some table values as words (`MRN` for `MR`, `HOME` for `H`/`PRN`, `CURRENT` as a name type). The channel maps the first two and leaves name type out; the code maps are at the top of the transformer script.
- **Identifier systems** are configured at the top of the transformer script. `simhospital.example.org` stands in for the hospital's own namespace; NHS numbers use the real NHS system.

Not converted yet: other ADT events (A02 transfer and A03 discharge are next), Location and Practitioner resources, PD1, AL1 allergies, PID-22 ethnic group, and a Provenance record linking each resource back to its v2 message.

### Working on the channel

Edit the transformer in the web administrator (**Channels → ADT to FHIR → FHIR transaction → Transformer**), deploy, and test. When you're happy, write the channel back into the repo and commit it:

```sh
engine/oie/channels/export.sh                # engine -> engine/oie/channels/*.xml
engine/oie/channels/import.sh                # engine/oie/channels/*.xml -> engine, and deploy
```

To test without waiting for Simulated Hospital, send the sample messages straight to the engine port (Python 3, no dependencies):

```sh
tools/mllp_send.py localhost 6661 samples/adt/a01_admit.hl7
curl 'http://localhost:8080/fhir/Patient?identifier=http://simhospital.example.org/fhir/sid/mrn|2590157853&_revinclude=Encounter:subject'
```

These bypass the gateway, so they don't land in `data/archive/`.

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

**OIE image.** The newest published OIE image is 4.5.2, but the [Web Support](https://github.com/gibson9583/oie-web-support-plugin) extension that provides the browser-based administrator needs 4.6.0. So `engine/oie/Dockerfile` builds the engine from the OIE 4.6.0 release tarball, the same way upstream's image is built, and bakes Web Support into `extensions/`. Both downloads are pinned by URL and SHA-256; to upgrade either, change both values together and run `docker compose build engine`. Moving an existing `engine-db` volume to a newer OIE upgrades its schema, and there's no going back.

**Pin your images.** `.env` uses `:latest` for HAPI to get you started. Once things work, pin them to exact versions so the lab doesn't change underneath you.

## Docker concepts in this repo

- **Multi-stage builds** (`gateway/Dockerfile`, `hospital/Dockerfile`): compile in a full Go image, ship only the binary on distroless. The gateway image is a few MB. `engine/oie/Dockerfile` uses a throwaway stage to download, verify (`ADD --checksum`) and unpack, so the final image only carries the result.
- **Cross-compilation**: `--platform=$BUILDPLATFORM` plus `TARGETARCH` builds arm64 images natively, without emulation.
- **Healthchecks and startup order**: `depends_on: condition: service_healthy`. Simulated Hospital exits if it can't connect at startup, so it waits for the gateway to report healthy. The gateway has no shell or curl, so it health-checks itself with `/gateway -healthcheck`.
- **Networks as security boundaries**: four networks. The hospital can only reach the gateway, and each database is only reachable by its own service.
- **Named volumes vs bind mounts**: databases use named volumes (managed by Docker). Config and the archive use bind mounts (files you edit or read).
- **Compose file layering**: `COMPOSE_FILE` in `.env` merges the base file with one engine file and one FHIR file. Run `docker compose config` to see the merged result.
- **Localhost-only ports**: every published port is bound to `127.0.0.1`, so nothing is exposed to your network.

## Swapping components

Anything that listens on the four feed ports can be the engine. Anything that serves FHIR REST at `http://fhir:8080/fhir` can be the FHIR server. To swap one, add a file like `stack/engine.waggle.yaml` that defines a service named `engine`, then change `COMPOSE_FILE` in `.env`. Nothing upstream needs to change.

## Raspberry Pi

Every image here is available for arm64: the gateway and hospital cross-compile, OIE is built from a Java tarball on the multi-arch Temurin image, Postgres publishes arm64 images, and HAPI does too (confirm with `docker manifest inspect hapiproject/hapi:latest`). A single Pi 5 with 8 GB can run the whole stack; the two JVMs are the heavy part.

To spread the stack across several Pis, the service and network layout carries over to Docker Swarm or k3s. Build the images with `docker buildx build --platform linux/arm64,linux/amd64` and push them to a registry the Pis can pull from.

## AWS

The layout maps one-to-one:

- Images go to ECR, and each service becomes an ECS Fargate task.
- `engine-db` and `fhir-db` become RDS Postgres.
- The four networks become subnets and security groups.
- MLLP is plain TCP, so the gateway and engine feeds sit behind a Network Load Balancer, not an Application Load Balancer.
- `data/archive` becomes S3.
- HealthLake can stand in as a third FHIR server to compare against.
