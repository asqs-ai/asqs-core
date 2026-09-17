using System;
using static System.Math;
using Sku = System.String;

namespace Minimal.Domain;

/// <summary>How far along a basket is.</summary>
public enum BasketState
{
    Open,
    Locked = 3,
    CheckedOut,
}

/// <summary>A line in a basket.</summary>
public record BasketLine(Sku Sku, int Quantity, decimal UnitPrice);

/// <summary>Raised when a basket changes.</summary>
public delegate void BasketChanged(BasketLine line, BasketState state);

public class Basket
{
    private readonly List<BasketLine> lines = new();

    /// <summary>The basket's identifier.</summary>
    public string Id { get; }

    public Basket(string id) => Id = id;

    public BasketLine this[int index] => lines[index];

    /// <summary>Adds a line and returns the running total.</summary>
    [Obsolete("use AddLine")]
    public decimal Add(BasketLine line, bool merge = true)
    {
        lines.Add(line);
        return Total();
    }

    public decimal Total() => lines.Sum(l => l.Quantity * l.UnitPrice);

    public static Basket operator +(Basket a, BasketLine b)
    {
        a.Add(b);
        return a;
    }

    public double Rounded() => Round((double)Total(), 2);
}
