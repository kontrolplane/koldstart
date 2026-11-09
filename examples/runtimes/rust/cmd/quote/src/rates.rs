//! What a parcel costs to send: the zone of its destination, the rate of its service to that
//! zone, and the quote made from them.

use serde::{Deserialize, Serialize};

/// The heaviest parcel the shop sends, in grams.
pub const MAXIMUM_WEIGHT_GRAMS: u64 = 30_000;

/// How fast a parcel goes.
#[derive(Clone, Copy, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Service {
    #[default]
    Standard,
    Express,
}

/// Where a destination is, as far as its price goes.
#[derive(Clone, Copy, Debug, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Zone {
    Domestic,
    Europe,
    World,
}

/// The price of one service to one zone, in euro cents, and the working days it takes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Rate {
    pub base_cents: u64,
    pub per_kilogram_cents: u64,
    pub days: u32,
}

/// The rates a quote is made from: `Published` is the shop's own, and the tests use a fake.
pub trait RateCard {
    /// The zone of a country, by its iso 3166 code, or none for one the shop does not send to.
    fn zone(&self, country: &str) -> Option<Zone>;
    /// The rate of a service to a zone, or none when the service does not go there.
    fn rate(&self, zone: Zone, service: Service) -> Option<Rate>;
}

/// A parcel to quote for, as a request sends it.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Parcel {
    pub destination: String,
    pub weight_grams: u64,
    #[serde(default)]
    pub service: Service,
}

/// What a parcel costs to send, and how long it takes.
#[derive(Debug, Serialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct Quote {
    pub destination: String,
    pub zone: Zone,
    pub service: Service,
    pub price_cents: u64,
    pub currency: &'static str,
    pub days: u32,
}

/// Why a parcel cannot be quoted for, in words the caller can act on.
#[derive(Debug, PartialEq, Eq)]
pub enum Refusal {
    Weight,
    Destination(String),
    Service(Service, Zone),
}

impl std::fmt::Display for Refusal {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Refusal::Weight => write!(
                formatter,
                "weightGrams is not from 1 to {MAXIMUM_WEIGHT_GRAMS}"
            ),
            Refusal::Destination(country) => {
                write!(formatter, "the shop does not send to {country}")
            }
            Refusal::Service(service, zone) => {
                write!(formatter, "{service:?} does not go to the {zone:?} zone")
            }
        }
    }
}

/// The quote for a parcel: the base price of its service to its zone, and the price of each
/// kilogram or part of one.
pub fn quote(card: &impl RateCard, parcel: &Parcel) -> Result<Quote, Refusal> {
    if parcel.weight_grams == 0 || parcel.weight_grams > MAXIMUM_WEIGHT_GRAMS {
        return Err(Refusal::Weight);
    }
    let destination = parcel.destination.to_ascii_uppercase();
    let zone = card
        .zone(&destination)
        .ok_or_else(|| Refusal::Destination(destination.clone()))?;
    let rate = card
        .rate(zone, parcel.service)
        .ok_or(Refusal::Service(parcel.service, zone))?;
    let kilograms = parcel.weight_grams.div_ceil(1_000);
    Ok(Quote {
        destination,
        zone,
        service: parcel.service,
        price_cents: rate.base_cents + rate.per_kilogram_cents * kilograms,
        currency: "EUR",
        days: rate.days,
    })
}

/// The countries of the european zone, besides the shop's own.
const EUROPE: &[&str] = &[
    "AT", "BE", "CH", "CZ", "DE", "DK", "ES", "FI", "FR", "GB", "IE", "IT", "NO", "PL", "PT", "SE",
];

/// The shop's own rates, from the netherlands. Express does not go outside europe.
pub struct Published;

impl RateCard for Published {
    fn zone(&self, country: &str) -> Option<Zone> {
        match country {
            "NL" => Some(Zone::Domestic),
            _ if EUROPE.contains(&country) => Some(Zone::Europe),
            _ if country.len() == 2 && country.bytes().all(|byte| byte.is_ascii_uppercase()) => {
                Some(Zone::World)
            }
            _ => None,
        }
    }

    fn rate(&self, zone: Zone, service: Service) -> Option<Rate> {
        match (zone, service) {
            (Zone::Domestic, Service::Standard) => Some(Rate {
                base_cents: 495,
                per_kilogram_cents: 50,
                days: 1,
            }),
            (Zone::Domestic, Service::Express) => Some(Rate {
                base_cents: 895,
                per_kilogram_cents: 75,
                days: 1,
            }),
            (Zone::Europe, Service::Standard) => Some(Rate {
                base_cents: 1095,
                per_kilogram_cents: 150,
                days: 4,
            }),
            (Zone::Europe, Service::Express) => Some(Rate {
                base_cents: 2495,
                per_kilogram_cents: 250,
                days: 2,
            }),
            (Zone::World, Service::Standard) => Some(Rate {
                base_cents: 2495,
                per_kilogram_cents: 450,
                days: 10,
            }),
            (Zone::World, Service::Express) => None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A card with one zone, `domestic` for `XX`, and standard alone, so a test says what it
    /// expects from the rates it sees.
    struct FakeCard;

    impl RateCard for FakeCard {
        fn zone(&self, country: &str) -> Option<Zone> {
            (country == "XX").then_some(Zone::Domestic)
        }

        fn rate(&self, _zone: Zone, service: Service) -> Option<Rate> {
            (service == Service::Standard).then_some(Rate {
                base_cents: 100,
                per_kilogram_cents: 10,
                days: 3,
            })
        }
    }

    fn parcel(destination: &str, weight_grams: u64, service: Service) -> Parcel {
        Parcel {
            destination: destination.to_string(),
            weight_grams,
            service,
        }
    }

    /// A quote is the base price and the price of each kilogram or part of one.
    #[test]
    fn prices_each_kilogram_or_part_of_one() {
        let examples = [
            (1, 110),
            (1_000, 110),
            (1_001, 120),
            (2_500, 130),
            (MAXIMUM_WEIGHT_GRAMS, 400),
        ];
        for (weight_grams, price_cents) in examples {
            let quoted =
                quote(&FakeCard, &parcel("XX", weight_grams, Service::Standard)).expect("a quote");
            assert_eq!(quoted.price_cents, price_cents, "{weight_grams} grams");
            assert_eq!(quoted.days, 3);
        }
    }

    /// A destination is read in either case, and answered in upper case.
    #[test]
    fn reads_the_destination_in_either_case() {
        let quoted = quote(&FakeCard, &parcel("xx", 500, Service::Standard)).expect("a quote");
        assert_eq!(quoted.destination, "XX");
    }

    /// A parcel of no weight, one too heavy, one to nowhere the card knows, or by a service
    /// that does not go there, is refused with why.
    #[test]
    fn refuses_what_it_cannot_quote_for() {
        let examples = [
            (parcel("XX", 0, Service::Standard), Refusal::Weight),
            (
                parcel("XX", MAXIMUM_WEIGHT_GRAMS + 1, Service::Standard),
                Refusal::Weight,
            ),
            (
                parcel("YY", 500, Service::Standard),
                Refusal::Destination("YY".to_string()),
            ),
            (
                parcel("XX", 500, Service::Express),
                Refusal::Service(Service::Express, Zone::Domestic),
            ),
        ];
        for (refused, why) in examples {
            assert_eq!(quote(&FakeCard, &refused), Err(why));
        }
    }

    /// The published card puts the netherlands, europe and the rest of the world in their zones,
    /// and sends express within europe alone.
    #[test]
    fn publishes_the_zones_of_the_shop() {
        assert_eq!(Published.zone("NL"), Some(Zone::Domestic));
        assert_eq!(Published.zone("DE"), Some(Zone::Europe));
        assert_eq!(Published.zone("JP"), Some(Zone::World));
        assert_eq!(Published.zone("Japan"), None);
        assert_eq!(Published.rate(Zone::World, Service::Express), None);
    }
}
