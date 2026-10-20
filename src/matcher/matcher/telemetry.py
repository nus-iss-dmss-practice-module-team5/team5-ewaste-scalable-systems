"""Read librdkafka statistics without broker calls or changes to offset handling."""


def lag_samples(stats, owned):
    """Return committed lag for owned partitions; negative/unavailable is unknown.

    The runner uses read_committed, so ls_offset is the visible end (not hi_offset).
    librdkafka consumer_lag is based on the committed offset, not prefetched data.
    """
    samples = []
    for topic, data in stats.get("topics", {}).items():
        for raw_partition, values in data.get("partitions", {}).items():
            partition = int(raw_partition)
            if (topic, partition) not in owned:
                continue
            committed = values.get("committed_offset", -1)
            end = values.get("ls_offset", -1)
            lag = values.get("consumer_lag", -1)
            known = all(isinstance(v, int) and v >= 0 for v in (committed, end, lag))
            samples.append(dict(topic=topic, partition=partition,
                                committed_offset=committed if committed >= 0 else None,
                                end_offset=end if end >= 0 else None,
                                consumer_lag=lag if known else None,
                                sample_available=known,
                                outcome="AVAILABLE" if known else "UNKNOWN"))
    return samples
