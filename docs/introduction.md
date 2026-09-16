# ipmap — a chart of the addresses, not the territory

*Most maps of the internet draw borders. This one records what is at each address.*

Every other way of describing IP space works in territories. A routing table, an allocation
registry, a geolocation database — each says *this block belongs to that thing*, and each is
right to, because allocation really is how the address space is carved up. Ranges are the
natural shape of ownership.

Observation has a different shape. When a scanner finds a host, a sensor sees a connection, or a
pipeline classifies something that answered, the result is attached to **one address**. Its
neighbour may be unrelated, unallocated, or simply unobserved. Run a hundred million of those
together and you do not get territories — you get a scatter, and the borders you could draw
around it would be longer than the list of points inside.

`ipmap` is for the scatter. It is a map in both senses the word carries: a chart of what is
where, and the data structure that gets you from a key to a value without thinking about it.

## What it actually is

A static, memory-mapped map from an IP address to a fixed-width value. You build it once from
the addresses you have, write it to a single file, and then any number of processes can open
that file and query it concurrently, with no allocation per lookup and no server in between.

The construction rests on one observation: **the high bits of an address do not need to be
stored.** Group entries by a prefix and find the group by array index, and the prefix is implicit
in *where the entry sits*. An IPv4 address grouped by its first three octets needs one byte, not
four. A 128-bit address grouped by its first 64 bits needs eight, not sixteen — and the prefix
itself is written once for every address that shares it, rather than once per address.

That is the whole trick, and it is worth about 40% against a flat array and considerably more
against a prefix structure, which for this shape of data does not merely fail to help but
actively costs more than storing every address individually.

## The channel nobody else offers

Libraries for prefix lookup are common and good, and `ipmap` does not compete with them — it is
the other half. Ask *which network owns this address* and you want longest-prefix match. Ask
*what did we record about this exact address* and prefix matching has nothing to match against.

The gap matters because the second question is the one that scales badly. Prefix tables are
small by nature; the internet has a few hundred thousand routable blocks. Observation sets have
no such ceiling, and the structures that serve prefixes well degrade exactly where observation
sets live.

## What stays yours

The value is **opaque bytes of a width you choose**. `ipmap` stores it, returns it, and never
looks inside. Whether those bytes are a score, a packed record, a timestamp, or an index into
something of your own is not the library's business — and deliberately so, because the moment a
storage layer learns what its payload means, it stops being usable by the next caller.

You also keep the build. There is no daemon, no background refresh, no network call. You decide
when an artifact is made and when a reader picks it up; the library's job ends at answering the
question.

---

**Read next:** [executive-summary](executive-summary.md) · [architecture](architecture.md) ·
[userguide](userguide.md) · [roadmap](roadmap.md)
