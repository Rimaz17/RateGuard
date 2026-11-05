-- Removes and returns one minute's usage counts (ADR 0018).
--
-- KEYS[1]  the minute's hash, rg:usage:<minute>
-- KEYS[2]  the index of minutes with counts, rg:usage:minutes
-- ARGV[1]  the minute, as Unix seconds at its start
--
-- Reading and deleting in one script means two instances rolling up at once
-- never both take the same counts. Counts added for the minute afterwards
-- recreate the hash and its index entry, and a later rollup takes them.
-- Returns the hash as field, value, field, value, ...

local counts = redis.call('HGETALL', KEYS[1])
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return counts
