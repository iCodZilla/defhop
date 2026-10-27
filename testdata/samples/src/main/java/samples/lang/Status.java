package samples.lang;

public enum Status {
    NEW,
    ACTIVE,
    CLOSED;

    public boolean isOpen() {
        return this == NEW || this == ACTIVE;
    }
}
