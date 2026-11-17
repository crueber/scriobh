//! An answerer that sends a byte every second or so must not keep a look at the server going: one look is over
//! within a fixed time, whatever arrives (a hang here would keep the launch from ever showing an error window).

use std::io::Write;
use std::net::TcpListener;
use std::thread;
use std::time::{Duration, Instant};

use scriobh_desktop::launch;

fn trickler(first: &'static [u8]) -> u16 {
    let l = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = l.local_addr().unwrap().port();
    thread::spawn(move || {
        for s in l.incoming() {
            let mut s = s.unwrap();
            thread::spawn(move || {
                let _ = s.write_all(first);
                loop {
                    thread::sleep(Duration::from_millis(1500));
                    if s.write_all(b"x").is_err() {
                        break;
                    }
                }
            });
        }
    });
    port
}

#[test]
fn a_trickling_answer_is_cut_off_at_the_deadline() {
    let port = trickler(b"HTTP/1.1 200 OK\r\n");
    let began = Instant::now();
    let got = launch::loopback_get(port, "/healthz", "X-Scriobh-Challenge: ab\r\n", 8192);
    let took = began.elapsed();
    assert!(got.is_err(), "a look that never finishes is an error, got {got:?}");
    assert!(took < Duration::from_secs(8), "the look took {took:?}");
}
