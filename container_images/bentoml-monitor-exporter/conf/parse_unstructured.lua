json = require "conf/json"

function parse(tag, timestamp, record)
	record = json.decode(record)
	if record["protocol_version"] == "1" or record["protocol_version"] == nil then
		timestamp = record['timestamp']
		record["bento_name"] = record["bento_meta"]["bento_name"]
		record["bento_version"] = record["bento_meta"]["bento_version"]
		record["monitor_name"] = record["bento_meta"]["monitor_name"]
		record["monitor_schema"] = json.encode(record["bento_meta"]["schema"])
		record["bento_meta"] = nil
	end
	return 1, timestamp, record
end
