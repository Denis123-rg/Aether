#![no_main]

use libfuzzer_sys::fuzz_target;
use prost::Message;

include!(concat!(env!("OUT_DIR"), "/aether.rs"));

fuzz_target!(|data: &[u8]| {
    let _ = ValidatedArb::decode(data);
    let _ = StreamArbsRequest::decode(data);
    let _ = HealthCheckRequest::decode(data);
    let _ = SetStateRequest::decode(data);
    let _ = ReloadConfigRequest::decode(data);
    if let Ok(arb) = ValidatedArb::decode(data) {
        let mut buf = Vec::new();
        let _ = arb.encode(&mut buf);
        let _ = ValidatedArb::decode(buf.as_slice());
    }
});
