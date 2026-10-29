package samples.lang;

/** Fixture: Java record components + accessors. */
public record Point(int x, int y) {
    public int sum() {
        return x + y;
    }
}
