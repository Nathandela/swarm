# Push-gateway operator runbook

## Observe

The admin plane is loopback-only:

```bash
curl --fail http://127.0.0.1:8451/healthz
curl --fail http://127.0.0.1:8451/readyz
curl --fail http://127.0.0.1:8451/metrics
```

Alert on readiness != 200, process/container restart loops, absence of a recent successful
retention sweep, storage growth, and sustained non-2xx counts. Metrics and logs are deliberately
aggregate and low-cardinality. Never add raw request paths: they contain opaque push addresses.
Never log FCM tokens, capability values, installation public keys, attestation tokens, or request
bodies. Short FCM/Play upstream failures must not page as readiness failures.

## Backup

The database and adjacent `.key` file are one recovery unit. A database without its exact key
cannot decrypt stored FCM tokens. Stop the service so the backup command can acquire the bbolt lock:

```bash
sudo systemctl stop swarm-pushgw
sudo -u swarm-pushgw /opt/swarm-pushgw/bin/swarm-pushgw backup \
  -db /var/lib/swarm-pushgw/pushgw.db \
  /var/backups/swarm-pushgw/pushgw-YYYYMMDD.tar
sudo systemctl start swarm-pushgw
```

Encrypt the archive under a separately managed backup key, copy it off-host, and apply the same
retention/access policy as a credentials database. The archive manifest checks both payloads.
Test restore regularly on an isolated host.

## Restore

Stop the service and move any existing database and `.key` aside as one recoverable pair. The
restore command refuses to overwrite either target and validates checksums, key length, and bbolt
buckets before publishing files:

```bash
sudo systemctl stop swarm-pushgw
sudo -u swarm-pushgw /opt/swarm-pushgw/bin/swarm-pushgw restore \
  -db /var/lib/swarm-pushgw/pushgw.db \
  /secure/restore/pushgw-YYYYMMDD.tar
sudo systemctl start swarm-pushgw
curl --fail http://127.0.0.1:8451/readyz
```

Exercise a registration and wake after restore. If the key is missing or differs from the running
process's loaded key, readiness fails rather than silently producing undecryptable restarts.

## Upgrade and rollback

Back up first. Pin an immutable `PUSHGW_VERSION`, pull/build it, validate Compose, then recreate the
gateway. Confirm readiness and a cellular background wake before declaring success. Rollback uses
the previous immutable image plus its compatible database; never restore only one of DB/key.
