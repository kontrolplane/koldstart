"""Tests of the quote handler and the pricing, against a tariff of the tests' own."""

import base64
import json
import logging
import unittest

from src.pricing import Parcel, QuoteRequest, Tariff, Zone, chargeable_grams, price
from src.quote import make_handler

TARIFF = Tariff(
    zones={
        "near": Zone(name="near", base_cents=500, cents_per_kilogram=100, standard_days=1),
        "far": Zone(name="far", base_cents=2000, cents_per_kilogram=500, standard_days=5),
    },
    countries={"NL": "near", "US": "far"},
    express_factor=1.5,
    express_days=1,
    volumetric_divisor=5000,
)


def event(body: dict | str, base64_encoded: bool = False) -> dict:
    """A request of the http api to POST /quotes with body, as json unless it is text already."""
    text = body if isinstance(body, str) else json.dumps(body)
    return {
        "routeKey": "POST /quotes",
        "body": base64.b64encode(text.encode()).decode() if base64_encoded else text,
        "isBase64Encoded": base64_encoded,
        "requestContext": {"requestId": "request-1"},
    }


def parcel(grams: int = 1000, length: int = 10, width: int = 10, height: int = 10) -> dict:
    """A parcel of a request, small enough by default that it is charged by its weight."""
    return {"weightGrams": grams, "lengthCentimetres": length, "widthCentimetres": width, "heightCentimetres": height}


class QuoteTest(unittest.TestCase):
    """The handler: what it answers for each kind of request."""

    def setUp(self):
        self.handler = make_handler(TARIFF)
        logging.disable(logging.CRITICAL)

    def tearDown(self):
        logging.disable(logging.NOTSET)

    def answer(self, request: dict) -> tuple[int, dict, dict]:
        response = self.handler(request, None)
        return response["statusCode"], response["headers"], json.loads(response["body"])

    def test_a_quote(self):
        status, headers, body = self.answer(event({"destination": "NL", "parcels": [parcel(2500)]}))
        self.assertEqual(status, 200)
        self.assertEqual(headers["x-request-id"], "request-1")
        self.assertEqual(body["totalCents"], 500 + 3 * 100)
        self.assertEqual(body["estimatedDays"], 1)

    def test_express_costs_more_and_is_faster(self):
        status, _, body = self.answer(event({"destination": "US", "service": "express", "parcels": [parcel(1000)]}))
        self.assertEqual(status, 200)
        self.assertEqual(body["totalCents"], (2000 + 500) * 1.5)
        self.assertEqual(body["estimatedDays"], 1)

    def test_a_body_in_base64(self):
        status, _, _ = self.answer(event({"destination": "NL", "parcels": [parcel()]}, base64_encoded=True))
        self.assertEqual(status, 200)

    def test_refusals(self):
        examples = [
            ("text that is not json", "parcels", ""),
            ("no parcels", {"destination": "NL", "parcels": []}, "parcels"),
            ("a country in lower case", {"destination": "nl", "parcels": [parcel()]}, "destination"),
            ("a parcel too heavy", {"destination": "NL", "parcels": [parcel(grams=40_000)]}, "parcels.0.weightGrams"),
            ("a service there is not", {"destination": "NL", "service": "overnight", "parcels": [parcel()]}, "service"),
            ("a field there is not", {"destination": "NL", "parcels": [parcel()], "coupon": "free"}, "coupon"),
        ]
        for name, body, field in examples:
            with self.subTest(name):
                status, headers, answer = self.answer(event(body))
                self.assertEqual(status, 400)
                self.assertEqual(headers["content-type"], "application/problem+json")
                self.assertIn(field, [error["field"] for error in answer.get("errors", [{"field": ""}])])

    def test_a_country_the_carrier_does_not_send_to(self):
        status, _, body = self.answer(event({"destination": "JP", "parcels": [parcel()]}))
        self.assertEqual(status, 422)
        self.assertIn("JP", body["title"])

    def test_a_failure_is_a_500_that_says_nothing_of_the_cause(self):
        handler = make_handler(None)
        response = handler(event({"destination": "NL", "parcels": [parcel()]}), None)
        self.assertEqual(response["statusCode"], 500)
        self.assertIn("request-1", response["body"])
        self.assertNotIn("NoneType", response["body"])


class PricingTest(unittest.TestCase):
    """The pricing: the weight a parcel is charged by, and what it costs."""

    def test_chargeable_grams(self):
        examples = [
            ("heavy and small, by its weight", parcel(grams=5000), 5000),
            ("light and large, by its volume", parcel(grams=500, length=50, width=40, height=30), 12_000),
        ]
        for name, given, want in examples:
            with self.subTest(name):
                self.assertEqual(chargeable_grams(Parcel(**given), 5000), want)

    def test_every_started_kilogram_is_charged(self):
        priced = price(QuoteRequest(destination="NL", parcels=[Parcel(**parcel(grams=1001))]), TARIFF)
        self.assertEqual(priced["parcels"][0]["priceCents"], 500 + 2 * 100)


if __name__ == "__main__":
    unittest.main()
