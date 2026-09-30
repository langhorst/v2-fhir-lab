# Sample messages

Test inputs for the engine channels. Send them with `tools/mllp_send.py`.

- `adt/a01_admit.hl7`: three ADT^A01 messages from Simulated Hospital's published sample output (`docs/artifacts/messages.out` in [google/simhospital](https://github.com/google/simhospital), Apache License 2.0). All data is synthetic.
- `adt/a03_discharge.hl7`: a discharge for the first A01's patient and visit, for checking how the ADT channel handles events it doesn't convert yet.
