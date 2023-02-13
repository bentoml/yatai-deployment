json = require "conf/json"

function parse(tag, timestamp, record)
	record = json.decode(record)
	timestamp = record['timestamp']
	record["bento_name"] = record["bento_meta"]["bento_name"]
	record["bento_version"] = record["bento_meta"]["bento_version"]
	record["monitor_name"] = record["bento_meta"]["monitor_name"]
	record["monitor_schema"] = json.encode(record["bento_meta"]["schema"])
	record["bento_meta"] = nil
	return 1, timestamp, record
end
