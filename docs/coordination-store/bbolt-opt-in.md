# bbolt Coordination Store

Gas City can run its managed bead store on an embedded bbolt file instead of
the default managed Dolt server. This mode is opt-in and intended for local
fresh-start use.

## Enable

Add the backend setting to `city.toml`:

```toml
[beads]
backend = "bbolt"
```

Leave `provider` empty or set to `bd`. The bbolt backend is ignored when
`provider = "file"` or `provider = "exec:<script>"`.

On the next `gc start`, Gas City creates the store at:

```text
.gc/state/bbolt/beads.bolt
```

Rig-scoped stores use the same path under each rig root.

## Fresh Start Semantics

The bbolt backend does not migrate existing Dolt bead state. Enabling it starts
with an empty store for each scope. Existing Dolt data remains on disk and is
available again if you revert to the default backend.

## Limitations

- No external Dolt sync or Dolt backup applies while bbolt is active.
- bbolt is single-process. A second process trying to open the same store waits
  briefly and then fails with a lock timeout.
- There is no migration between Dolt and bbolt stores.

## Revert

Remove the `backend` line or set it back to `dolt`:

```toml
[beads]
backend = "dolt"
```

Then restart the city. Gas City returns to the managed Dolt backend and the
previous Dolt state remains available.
