"""Prices the parcels of a shipment: what a request may ask for, and what each parcel costs."""

from dataclasses import dataclass
from math import ceil
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field


class Parcel(BaseModel):
    """One parcel, in grams and centimetres."""

    model_config = ConfigDict(extra="forbid")

    weightGrams: int = Field(ge=1, le=30_000)
    lengthCentimetres: int = Field(ge=1, le=150)
    widthCentimetres: int = Field(ge=1, le=150)
    heightCentimetres: int = Field(ge=1, le=150)


class QuoteRequest(BaseModel):
    """What a client asks a price for: where the parcels go, how fast, and the parcels."""

    model_config = ConfigDict(extra="forbid")

    destination: str = Field(pattern=r"^[A-Z]{2}$", description="the country, as iso 3166 names it")
    service: Literal["standard", "express"] = "standard"
    parcels: list[Parcel] = Field(min_length=1, max_length=10)


@dataclass(frozen=True)
class Zone:
    """What sending a parcel to the countries of a zone costs, in cents, and how long it takes."""

    name: str
    base_cents: int
    cents_per_kilogram: int
    standard_days: int


@dataclass(frozen=True)
class Tariff:
    """The zones a carrier prices by, with the countries of each, and what express adds."""

    zones: dict[str, Zone]
    countries: dict[str, str]
    express_factor: float
    express_days: int
    volumetric_divisor: int

    def zone(self, country: str) -> Zone | None:
        """The zone of country, or None when the carrier does not send there."""
        name = self.countries.get(country)
        return None if name is None else self.zones[name]


def chargeable_grams(parcel: Parcel, divisor: int) -> int:
    """The weight a parcel is charged by: its own, or its volume's when that is more."""
    volumetric = ceil(parcel.lengthCentimetres * parcel.widthCentimetres * parcel.heightCentimetres * 1000 / divisor)
    return max(parcel.weightGrams, volumetric)


def price(request: QuoteRequest, tariff: Tariff) -> dict | None:
    """The quote for request, parcel by parcel, or None when the carrier does not send there."""
    zone = tariff.zone(request.destination)
    if zone is None:
        return None
    factor = tariff.express_factor if request.service == "express" else 1.0
    parcels = []
    for parcel in request.parcels:
        grams = chargeable_grams(parcel, tariff.volumetric_divisor)
        cents = ceil((zone.base_cents + zone.cents_per_kilogram * ceil(grams / 1000)) * factor)
        parcels.append({"chargeableGrams": grams, "priceCents": cents})
    return {
        "destination": request.destination,
        "zone": zone.name,
        "service": request.service,
        "currency": "EUR",
        "parcels": parcels,
        "totalCents": sum(parcel["priceCents"] for parcel in parcels),
        "estimatedDays": tariff.express_days if request.service == "express" else zone.standard_days,
    }


# The carrier's prices: the ones the function runs with, which the tests replace with their own.
TARIFF = Tariff(
    zones={
        "domestic": Zone(name="domestic", base_cents=495, cents_per_kilogram=40, standard_days=1),
        "europe": Zone(name="europe", base_cents=995, cents_per_kilogram=150, standard_days=3),
        "world": Zone(name="world", base_cents=2495, cents_per_kilogram=600, standard_days=7),
    },
    countries={
        "NL": "domestic",
        **{country: "europe" for country in ("BE", "DE", "DK", "ES", "FR", "GB", "IE", "IT", "LU", "AT", "PL", "PT", "SE")},
        **{country: "world" for country in ("AU", "CA", "JP", "NZ", "SG", "US")},
    },
    express_factor=1.6,
    express_days=1,
    volumetric_divisor=5000,
)
