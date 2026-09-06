# Dragonfly as the store — for shape, not scale

concord stores read-hashes and intent records in Dragonfly rather than a Postgres table inside Hindsight. The intent state is ephemeral, written at a high rate, and needs both literal path-token matching and fast expiry — a shape that fits a Redis-compatible in-memory store well. The measured live keyspace is tiny (~12KB at the observed peak of 6 concurrent subagents, ~120KB with 10x headroom), so **scale does not justify Dragonfly and the README must say so plainly**: the choice is about access shape and operational fit, not volume.
