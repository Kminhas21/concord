# Standalone Go service, not a Hindsight extension

concord is a standalone Go service that runs alongside a self-hosted Hindsight instance without being coupled to it. We rejected building it as a Hindsight extension endpoint because their extensions load as Python modules into their API process, which would change concord's language to Python and couple concord's releases to Hindsight's. Durable memory and retrieval over past sessions remain Hindsight's job; concord owns only the in-flight coordination state.
