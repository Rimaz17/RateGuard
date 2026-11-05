-- Adds usage counts for one minute (ADR 0018).
--
-- KEYS[1]  the minute's hash, rg:usage:<minute>
-- KEYS[2]  the index of minutes with counts, rg:usage:minutes
-- ARGV[1]  the minute, as Unix seconds at its start
-- ARGV[2..] field, increment pairs; a field is <caller>:a (allowed) or
--           <caller>:r (rejected)
--
-- A script, rather than a pipeline, so a batch is added whole or not at
-- all. Returns the number of fields added to.

for i = 2, #ARGV, 2 do
  redis.call('HINCRBY', KEYS[1], ARGV[i], ARGV[i + 1])
end
redis.call('ZADD', KEYS[2], ARGV[1], ARGV[1])
return (#ARGV - 1) / 2
